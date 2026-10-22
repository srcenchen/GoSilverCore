package receiver

import (
	"context"
	"errors"
	"fmt"
	_const "go-silver-core/internal/const"
	"go-silver-core/internal/gsp_sdk/client"
	"go-silver-core/internal/gsp_sdk/model"
	"go-silver-core/internal/gsp_sdk/server"
	"math/rand/v2"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

type BlockSource struct {
	Index int64  `json:"index"`
	Addr  string `json:"addr"`
}

type Progress struct {
	Active   []BlockSource `json:"active,omitempty"`
	FileName string        `json:"fileName"`
	Total    int64         `json:"total"`
	Done     int64         `json:"done"`
	Bytes    int64         `json:"bytes"`
	Speed    int64         `json:"speed"`
	Err      error         `json:"-"`
	Finished bool          `json:"finished"`
}
type ProgressFunc func(Progress)

func Run(ctx context.Context, addr, dir string, s *server.Session, sdk client.GspSdk, meta model.GetFileStatusResp, on ProgressFunc) error {
	if dir == "" {
		dir = "."
	}
	return RunTo(ctx, addr, filepath.Join(dir, filepath.Base(meta.FileName)), s, sdk, meta, on)
}

// RunTo uses a temporary file and joins every worker before cleanup. Completed
// files stay open through Session so the receiver continues seeding.
func RunTo(ctx context.Context, addr, target string, s *server.Session, sdk client.GspSdk, meta model.GetFileStatusResp, on ProgressFunc, overwrite ...bool) error {
	if meta.FileName == "" || filepath.Base(meta.FileName) != meta.FileName || meta.FileSize < 0 || meta.FileSize > 1<<50 || meta.ChunkSize != _const.ChunkSize || meta.ChunkNum != (meta.FileSize+_const.ChunkSize-1)/_const.ChunkSize {
		return errors.New("invalid file metadata")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(target), ".gosilver-*.part")
	if err != nil {
		return err
	}
	keep := false
	defer func() {
		if !keep {
			s.ClearChunks()
			_ = f.Close()
			_ = os.Remove(f.Name())
		}
	}()
	if err = f.Truncate(meta.FileSize); err != nil {
		return err
	}
	s.BeSendSub(f)
	ck := s.GetChunk()
	sdk = sdk.WithContext(ctx)
	// Shuffle chunk order without constructing one goroutine per chunk.
	count := meta.ChunkNum
	start, step := int64(0), int64(1)
	if count > 0 {
		start = rand.Int64N(count)
	}
	if count > 1 {
		step = rand.Int64N(count-1) + 1
		for gcd(step, count) != 1 {
			step = step%(count-1) + 1
		}
	}
	workCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	sdk = sdk.WithContext(workCtx)
	jobs := make(chan int64)
	priority := make(chan int64, 4)
	s.SetPrefetchHandler(func(index int64) {
		if index >= 0 && index < count {
			select {
			case priority <- index:
			default:
			}
		}
	})
	defer s.SetPrefetchHandler(nil)
	var wg sync.WaitGroup
	var mu sync.Mutex
	var failure error
	var done, received int64
	started := time.Now()
	active := map[int64]string{}
	claimed := map[int64]bool{}
	emitLocked := func() {
		if on == nil {
			return
		}
		sources := make([]BlockSource, 0, len(active))
		for i, addr := range active {
			sources = append(sources, BlockSource{Index: i, Addr: addr})
		}
		sort.Slice(sources, func(i, j int) bool { return sources[i].Index < sources[j].Index })
		speed := int64(float64(received) * 8 / time.Since(started).Seconds() / 1e6)
		on(Progress{FileName: meta.FileName, Total: meta.ChunkNum, Done: done, Bytes: received, Speed: speed, Active: sources})
	}
	for range min(int64(5), count) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range jobs {
				mu.Lock()
				skip := claimed[i] || s.HasChunk(i)
				if !skip {
					claimed[i] = true
				}
				mu.Unlock()
				if skip {
					continue
				}
				var last error
				for attempt := 0; attempt < 3; attempt++ {
					if err := workCtx.Err(); err != nil {
						return
					}
					location, e := sdk.WantChunk(i)
					if e != nil {
						last = e
					} else {
						candidates := location.Sources
						if len(candidates) == 0 {
							candidates = []model.SourceCandidate{{Addr: location.Addr, UUID: location.UUID}}
						}
						hasCentral := false
						for _, candidate := range candidates {
							if candidate.Addr == "" || candidate.Addr == addr {
								hasCentral = true
							}
						}
						if !hasCentral {
							candidates = append(append([]model.SourceCandidate(nil), candidates...), model.SourceCandidate{})
						}
						var n int
						var checksum uint32
						providerID := ""
						var sampleSpeed int64
						for _, candidate := range candidates {
							if workCtx.Err() != nil {
								return
							}
							provider := candidate.Addr
							if provider == "" {
								provider = addr
							}
							providerID = candidate.UUID
							mu.Lock()
							active[i] = provider
							emitLocked()
							mu.Unlock()
							attemptStart := time.Now()
							n, checksum, e = sdk.FetchChunk(provider, i, &ck)
							if e == nil {
								sampleSpeed = int64(float64(n) * 8 / time.Since(attemptStart).Seconds() / 1e6)
								break
							}
							_ = sdk.ReportPeer(s.UUID, providerID, 0, "failed", i)
						}
						mu.Lock()
						delete(active, i)
						mu.Unlock()
						if e == nil {
							s.AddChunk(i, checksum)
							_ = sdk.ReportChunk(s.UUID, i)
							mu.Lock()
							delete(claimed, i)
							done++
							received += int64(n)
							emitLocked()
							mu.Unlock()
							_ = sdk.ReportPeer(s.UUID, providerID, sampleSpeed, "done", i)
							last = nil
							break
						}
						last = e
					}
					if attempt < 2 {
						timer := time.NewTimer(time.Duration(attempt+1) * 200 * time.Millisecond)
						select {
						case <-workCtx.Done():
							timer.Stop()
							return
						case <-timer.C:
						}
					}
				}
				if last != nil {
					mu.Lock()
					if failure == nil {
						failure = fmt.Errorf("chunk %d failed after 3 attempts: %w", i, last)
					}
					mu.Unlock()
					cancel()
					return
				}
			}
		}()
	}
feed:
	for n, i := int64(0), start; n < count; n, i = n+1, (i+step)%max(int64(1), count) {
		select {
		case urgent := <-priority:
			select {
			case jobs <- urgent:
			case <-workCtx.Done():
				break feed
			}
		default:
		}
		select {
		case jobs <- i:
		case <-workCtx.Done():
			break feed
		}
	}
	close(jobs)
	wg.Wait()
	if err = ctx.Err(); err != nil {
		return err
	}
	if failure != nil {
		return failure
	}
	if err = f.Sync(); err != nil {
		return err
	}
	if err = ctx.Err(); err != nil {
		return err
	}
	if len(overwrite) > 0 && !overwrite[0] {
		if _, e := os.Lstat(target); e == nil {
			return fmt.Errorf("目标文件已存在，未允许覆盖: %s", target)
		} else if !os.IsNotExist(e) {
			return e
		}
	}
	if err = os.Rename(f.Name(), target); err != nil {
		return err
	}
	s.RetainFile(f)
	keep = true
	if on != nil {
		on(Progress{FileName: meta.FileName, Total: meta.ChunkNum, Done: meta.ChunkNum, Bytes: meta.FileSize, Finished: true})
	}
	return nil
}

func gcd(a, b int64) int64 {
	for b != 0 {
		a, b = b, a%b
	}
	return a
}
