import test from 'node:test';
import assert from 'node:assert/strict';
import { diagnosticCollector } from './browser-diagnostics.mjs';
test('privacy verdict waits for delayed object arguments and exposes only safe counts', async () => {
  let release;
  const delayed = new Promise(resolve => { release = resolve; });
  const secret = 'a'.repeat(64), collector = diagnosticCollector(['private-cell']);
  collector.console({text:()=> 'object', args:()=>[{jsonValue:()=>delayed}]});
  let finished = false;
  const verdict = collector.drain().then(value=>{finished=true; return value;});
  await Promise.resolve(); await Promise.resolve();
  assert.equal(finished,false);
  release({handle:secret, cell:'private-cell'});
  const safe = await verdict;
  assert.deepEqual(safe,{consoleCount:1,pageErrorCount:0,privateLeak:true,scanFailed:false});
  assert.equal(JSON.stringify(safe).includes(secret),false);
});
test('text capabilities and failed scans fail closed without retaining raw errors',async()=>{
 const collector=diagnosticCollector([]);
 collector.console({text:()=> 'b'.repeat(64),args:()=>[]});
 collector.console({text:()=> 'safe',args:()=>[{jsonValue:async()=>{throw Error('sensitive');}}]});
 collector.pageError();
 assert.deepEqual(await collector.drain(),{consoleCount:2,pageErrorCount:1,privateLeak:true,scanFailed:true});
});
