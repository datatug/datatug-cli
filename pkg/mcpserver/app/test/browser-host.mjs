import { AppBridge, PostMessageTransport, buildAllowAttribute } from '@modelcontextprotocol/ext-apps/app-bridge';
// Test host only. This module is outside the production App bundle graph.
const config = await (await fetch('/config')).json();
let bridge, frame, lastResult;
const rpc = async (body, signal) => {
  const response = await fetch('/rpc', { method: 'POST', headers: { 'Content-Type': 'application/json', 'X-Test-Token': config.token }, body: JSON.stringify(body), signal });
  if (!response.ok) throw Error('Acceptance broker refused request');
  return response.json();
};
window.acceptance = {
  async open(mode = 'real', theme = 'light') {
    if (bridge) { await bridge.teardownResource({}).catch(() => {}); await bridge.close(); frame.remove(); }
    const resource = await rpc({ op: 'resource' });
    const ui = resource.contents[0]._meta.ui;
    frame = document.createElement('iframe'); frame.id = 'sandbox'; frame.title = 'Official MCP Apps sandbox'; frame.style.cssText = 'width:100%;height:720px;border:0';
    frame.setAttribute('sandbox', 'allow-scripts allow-same-origin allow-forms');
    frame.setAttribute('allow', buildAllowAttribute(ui.permissions));
    bridge = new AppBridge(null, { name: 'DataTug acceptance host', version: '1' }, { serverTools: {} }, { hostContext: { theme } });
    bridge.oncalltool = (params, extra) => rpc({ op: 'call', name: params.name, arguments: params.arguments }, extra.mcpReq.signal);
    bridge.onerror = () => { window.hostError = true; };
    const initialized = new Promise((yes, no) => {
      const timer = setTimeout(() => no(Error('Official App handshake timed out')), 15000);
      bridge.onsandboxready = () => { void bridge.sendSandboxResourceReady({ html: resource.contents[0].text, sandbox: 'allow-scripts allow-same-origin allow-forms', permissions: ui.permissions }); };
      bridge.oninitialized = async () => {
        try {
          lastResult = await rpc({ op: 'initial', mode });
          await bridge.sendToolInput({ arguments: { queryId: mode === 'real' ? 'customers/customer-purchases-by-genre' : 'acceptance-sentinel', parameters: { CustomerId: '1' } } });
          await bridge.sendToolResult(lastResult); clearTimeout(timer); yes();
        } catch { clearTimeout(timer); no(Error('Initial private result failed')); }
      };
    });
    document.getElementById('view').append(frame);
    await bridge.connect(new PostMessageTransport(frame.contentWindow, frame.contentWindow));
    frame.src = config.sandboxOrigin + '/sandbox.html';
    await initialized;
    return { origin: config.sandboxOrigin, mode };
  },
  theme: theme => bridge.sendHostContextChange({ theme }),
  late: () => bridge.sendToolResult(lastResult),
  async teardown() { await bridge.teardownResource({}); await bridge.close(); },
  control: action => rpc({ op: 'control', action }),
  report: () => rpc({ op: 'report' }),
  async siblingForgery() {
    const sibling = document.createElement('iframe'); sibling.id = 'sibling';
    const done = new Promise(yes => {
      const listener = event => {
        if (event.source === sibling.contentWindow && event.data === 'acceptance-forgery-sent') { window.removeEventListener('message',listener); yes(); }
      };
      window.addEventListener('message',listener);
    });
    sibling.srcdoc = `<script>const attack={jsonrpc:'2.0',id:94379,method:'tools/call',params:{name:'datatug_apps_result',arguments:{handle:'${'f'.repeat(64)}'}}};parent.postMessage(attack,'*');parent.frames[0].postMessage(attack,'*');parent.frames[0].frames[0].postMessage(attack,'*');parent.postMessage('acceptance-forgery-sent','*')<\/script>`;
    document.body.append(sibling); await done; sibling.remove();
  },
};
window.acceptanceReady = true;
