const {test}=require('node:test');
const assert=require('node:assert/strict');
const vm=require('node:vm');
const fs=require('node:fs');
const path=require('node:path');
const script=fs.readFileSync(path.join(__dirname,'../../internal/webui/static/session.js'),'utf8');
function harness(fetch){
 const meta={content:'old-token'},sources=[];
 class EventSource{constructor(url){this.url=url;this.closed=false;sources.push(this)}close(){this.closed=true}}
 const context={window:{},document:{querySelector:()=>meta},fetch,AbortController,DOMException,setTimeout,clearTimeout,EventSource,console};vm.createContext(context);vm.runInContext(script,context);
 return{session:context.window.GoSilverSession,meta,sources};
}
const response=(status,value)=>({status,ok:status>=200&&status<300,json:async()=>value});
test('refreshes rejected session and retries a mutation exactly once',async()=>{
 let attempts=0,refreshes=0,writes=0;
 const h=harness(async(url,options)=>{if(url==='/api/session'){refreshes++;return response(200,{token:'new-token'})}attempts++;if(options.headers['X-GoSilver-Token']==='old-token')return response(403,{code:'session_expired'});writes++;assert.equal(options.body,'{"action":"dispatch.auto"}');return response(202,{started:true})});
 assert.equal((await h.session.api('action',{action:'dispatch.auto'})).started,true);assert.equal(attempts,2);assert.equal(writes,1);assert.equal(refreshes,1);assert.equal(h.meta.content,'new-token');
});
test('does not replay an uncertain network mutation or refresh other forbidden requests',async()=>{
 let calls=0;const h=harness(async()=>{calls++;throw new Error('connection lost')});await assert.rejects(h.session.api('action',{}),/connection lost/);assert.equal(calls,1);
 const denied=harness(async()=>{calls++;return response(403,{error:'cross origin'})});await assert.rejects(denied.session.api('state'),/cross origin/);assert.equal(calls,2);
});
test('simultaneous expired requests share one refresh',async()=>{
 let refreshes=0;const h=harness(async(url,options)=>{if(url==='/api/session'){refreshes++;await new Promise(resolve=>setTimeout(resolve,10));return response(200,{token:'new-token'})}return options.headers['X-GoSilver-Token']==='old-token'?response(403,{code:'session_expired'}):response(200,{ok:true})});await Promise.all([h.session.api('state'),h.session.api('local')]);assert.equal(refreshes,1);
});
test('reopens events with a new token after process restart without reloading the page',async()=>{
 const h=harness(async()=>response(200,{token:'restarted-token'}));const states=[];const events=h.session.connectEvents(value=>states.push(value),()=>{});h.sources[0].onerror();await new Promise(resolve=>setTimeout(resolve,650));assert.equal(h.sources[0].closed,true);assert.equal(h.sources.length,2);assert.match(h.sources[1].url,/token=restarted-token/);h.sources[1].onmessage({data:'{"ready":true}'});assert.equal(states[0].ready,true);events.close();
});
