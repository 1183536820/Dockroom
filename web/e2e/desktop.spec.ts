import {test,expect,chromium} from '@playwright/test';
import {spawn} from 'node:child_process';
import {mkdtemp,mkdir,lstat,writeFile,readFile} from 'node:fs/promises';
import {createServer} from 'node:net';
import {join,resolve} from 'node:path';
import {randomUUID} from 'node:crypto';
import {fileURLToPath} from 'node:url';

const bin=fileURLToPath(new URL('../../.tools/go/bin/go.exe',import.meta.url));
const artifacts=fileURLToPath(new URL('../../.local/evidence/',import.meta.url));
async function port(){const s=createServer();await new Promise<void>((ok,fail)=>{s.once('error',fail);s.listen(0,'127.0.0.1',()=>ok());});const addr=s.address() as {port:number};await new Promise<void>(ok=>s.close(()=>ok()));return addr.port;}
function stop(p:ReturnType<typeof spawn>){
  if(p.exitCode!==null||p.signalCode!==null)return Promise.resolve();
  return new Promise<void>((done,fail)=>{const timeout=setTimeout(()=>fail(new Error('launcher did not stop')),6000);p.once('exit',()=>{clearTimeout(timeout);done();});spawn('taskkill',['/PID',String(p.pid),'/T','/F'],{windowsHide:true,stdio:'ignore'});});
}

test('Windows pairing, dual-role routing, reconnect, and host replacement',async()=>{
  test.skip(process.platform!=='win32','Windows launcher');
  test.setTimeout(60000);
  const base=await mkdtemp(resolve('../.local/desktop-test-'));await mkdir(artifacts,{recursive:true});
  const processes:ReturnType<typeof spawn>[]=[];
  const browser=await chromium.launch({channel:process.env.DOCKROOM_BROWSER_CHANNEL||undefined});
  async function launch(label:string,reopening=false){
    const parent=join(base,label);await mkdir(parent,{recursive:true});
    const p=spawn(bin,['run','-tags','dockroom_fixture','../cmd/hub'],{windowsHide:true,env:{...process.env,DOCKROOM_TEST_DESKTOP_BASE:parent},stdio:['ignore','pipe','pipe']});processes.push(p);
    const address=await new Promise<string>((done,fail)=>{
      const timeout=setTimeout(()=>fail(new Error('launcher did not open setup page')),8000);let text='',err='';
      p.stderr?.on('data',b=>{err+=String(b);});p.once('error',fail);p.once('exit',()=>{clearTimeout(timeout);fail(new Error(`launcher exited: ${err}`));});
      p.stdout?.on('data',b=>{text+=String(b);if(text.includes('\n')){clearTimeout(timeout);done(text.trim());}});
    });
    const context=await browser.newContext({viewport:{width:1180,height:720}});const page=await context.newPage();page.setDefaultTimeout(8000);
    await page.goto(address);
    if(!reopening){await expect(page.getByRole('heading',{name:'电脑连接'})).toBeVisible();await expect(page.getByRole('button',{name:'退出渡柜'})).toBeVisible();}
    return {page,address:new URL(address),parent,p};
  }
  try {
    const {page:host,address}=await launch('host');const {page:peer,parent,p:peerProcess}=await launch('peer');
    await peer.screenshot({path:join(artifacts,'windows-pairing.png')});
    await host.getByLabel('局域网地址').selectOption('127.0.0.1');
    const outside=await browser.newContext();const blocked=await outside.request.get(`${address.origin}/local/v1/status`);expect(blocked.status()).toBe(401);
    const wrong=await outside.request.post(`${address.origin}/local/v1/open`,{headers:{Origin:'https://not-this-app.invalid','X-Local-Key':address.hash.slice(1)}});expect(wrong.status()).toBe(403);
    const password=`fixture-${randomUUID()}`;
    await host.getByLabel('管理员口令',{exact:true}).fill(password);
    await host.getByText('高级设置：端口').click();
    await host.getByLabel('工作台及接入').fill(String(await port()));const device=await port();await host.getByLabel('设备连接',{exact:true}).fill(String(device));
    await host.getByRole('button',{name:'开启主机'}).click();await host.getByRole('button',{name:'生成连接码和密钥'}).click();
    const emitted=host.getByRole('textbox',{name:'发给另一台电脑的连接码'});await expect(emitted).toHaveValue(/^DG2\./);const code=await emitted.inputValue();
    const secret=host.getByLabel('本次生成的连接密钥',{exact:true});await expect(secret).toHaveValue(/^[A-Za-z0-9_-]{43}$/);const key=await secret.inputValue();
    expect(code.includes(key)).toBe(false);
    const joinCode=peer.getByRole('textbox',{name:'主机连接码'});
    const waiting=await host.context().newPage();await waiting.goto(address.origin);
    const remotePane=waiting.getByRole('article',{name:'右栏目录'});
    await expect(remotePane.getByText('等待对方电脑共享文件夹')).toBeVisible();
    const enter=async(page:typeof peer,code:string,key:string)=>{
      await page.getByRole('textbox',{name:'主机连接码'}).fill(code);await page.getByLabel('连接密钥',{exact:true}).fill(key);
      await page.getByRole('button',{name:'连接主机'}).click();
    };
    await enter(peer,'坏码',key);await expect(peer.getByText(/连接码无效/)).toBeVisible();
    const wrongKey=(key[0]==='a'?'b':'a')+key.slice(1);
    await enter(peer,code,wrongKey);await expect(peer.getByText(/连接密钥错误/)).toBeVisible();
    await expect(joinCode).toBeVisible();
    await enter(peer,code,key);await expect(peer.getByRole('heading',{name:'磁盘文件传输'})).toBeVisible();
    await expect(peer.getByRole('article').nth(1).getByRole('button',{name:/^[A-Z]:/}).first()).toBeVisible();
    const src=join(base,'未共享源'),dst=join(base,'未共享目标');await mkdir(src);await mkdir(dst);
    await writeFile(join(src,'发送.txt'),'forward');await writeFile(join(dst,'下载.txt'),'back');
    const panes=peer.getByRole('article');for(const [i,path] of [src,dst].entries()){await panes.nth(i).getByLabel('文件夹路径').fill(path);await panes.nth(i).getByText('打开目录',{exact:true}).click()}
    await panes.nth(0).getByRole('button',{name:'发送.txt',exact:true}).click();await panes.nth(0).getByText('发送到对方').click();
    await expect(panes.nth(1).getByText('发送.txt',{exact:true})).toBeVisible();expect(await readFile(join(dst,'发送.txt'),'utf8')).toBe('forward');
    await panes.nth(0).getByText('发送到对方').click();await expect(peer.getByRole('status')).toContainText('同名');expect(await readFile(join(dst,'发送.txt'),'utf8')).toBe('forward');
    await panes.nth(1).getByRole('button',{name:'下载.txt',exact:true}).click();await panes.nth(1).getByText('下载到本机').click();
    await expect(panes.nth(0).getByText('下载.txt',{exact:true})).toBeVisible();expect(await readFile(join(src,'下载.txt'),'utf8')).toBe('back');
    await peer.goto(new URL('/connected/',peer.url()).href);
    const endpoint=await peer.evaluate(()=>fetch('/local/v1/status').then(r=>r.json()));
    expect(endpoint.mode).toBe('readWrite');
    await writeFile(join(endpoint.folder,'接入验证.txt'),'fixture');
    await expect(remotePane.getByText('对方电脑',{exact:true})).toBeVisible({timeout:10000});
    await remotePane.getByRole('button',{name:'刷新',exact:true}).click();
    await expect(remotePane.getByText('接入验证.txt',{exact:true})).toBeVisible();await waiting.close();
    const local=await host.evaluate(()=>fetch('/local/v1/status').then(r=>r.json()));
    const defaultFolder=local.localFolders.find((x:{id:string})=>x.id==='dockroom-host/files');expect(defaultFolder.mode).toBe('readWrite');
    const hostExtra=join(base,'额外共享');await mkdir(hostExtra);await writeFile(join(hostExtra,'额外文件.txt'),'another fixture');
    const extra=await host.evaluate(async(folder)=>{
      const r=await fetch('/local/v1/add-folder',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify({folder})});return r.status;
    },hostExtra);expect(extra).toBe(200);
    await host.getByRole('link',{name:'打开本机双栏',exact:true}).click();
    const left=host.getByRole('article',{name:'左栏目录'});
    expect((await lstat(endpoint.folder)).isDirectory()).toBe(true);
    expect(await readFile(join(endpoint.folder,'接入验证.txt'),'utf8')).toBe('fixture');
    const saved=await readFile(join(parent,'Dockroom','agent-settings.json'),'utf8');expect(saved.includes(code)).toBe(false);expect(saved.includes(key)).toBe(false);
    const right=host.getByRole('article',{name:'右栏目录'});
    await left.getByRole('combobox',{name:'共享文件夹',exact:true}).selectOption({label:'额外共享'});
    await left.getByText('额外文件.txt',{exact:true}).click();await left.getByRole('button',{name:'发送到对方'}).click();
    await expect(right.getByText('额外文件.txt',{exact:true})).toBeVisible();
    expect(await readFile(join(endpoint.folder,'额外文件.txt'),'utf8')).toBe('another fixture');
    await left.getByRole('combobox',{name:'共享文件夹',exact:true}).selectOption('files');
    await right.getByText('接入验证.txt',{exact:true}).click();await right.getByRole('button',{name:'下载到本机'}).click();
    await expect(left.getByText('接入验证.txt',{exact:true})).toBeVisible();
    expect(await readFile(join(defaultFolder.path,'接入验证.txt'),'utf8')).toBe('fixture');
    const peerRight=peer.getByRole('article',{name:'右栏目录'});
    await peerRight.getByRole('button',{name:'刷新',exact:true}).click();
    await expect(peerRight.getByText('接入验证.txt',{exact:true})).toBeVisible();
    await writeFile(join(defaultFolder.path,'从主机复制.txt'),'fixture return');
    await peerRight.getByRole('button',{name:'刷新',exact:true}).click();
    await peerRight.getByRole('checkbox',{name:'选择 从主机复制.txt'}).check();
    await peerRight.getByRole('button',{name:'下载到本机'}).click();
    const peerLeft=peer.getByRole('article',{name:'左栏目录'});
    await expect(peerLeft.getByText('从主机复制.txt',{exact:true})).toBeVisible();expect(await readFile(join(endpoint.folder,'从主机复制.txt'),'utf8')).toBe('fixture return');
    await host.getByRole('button',{name:'设备管理',exact:true}).click();
    await expect(host.getByRole('button',{name:'批准连接'})).toHaveCount(0);
    host.once('dialog',d=>d.accept());await host.getByRole('button',{name:'更换密钥'}).click();
    const replacement=host.getByLabel('本次生成的连接密钥',{exact:true});await expect(replacement).not.toHaveValue(key);const newKey=await replacement.inputValue();expect(newKey).toHaveLength(43);
    await expect(host.getByRole('textbox',{name:'主机连接码'})).toHaveValue(code);
    const third=await launch('third');await enter(third.page,code,key);await expect(third.page.getByText(/连接密钥错误/)).toBeVisible();
    await enter(third.page,code,newKey);await third.page.waitForURL('**/computer/');await third.page.goto(new URL('/connected/',third.page.url()).href);
    const thirdNodes=await third.page.evaluate(()=>fetch('/connected/api/v1/nodes').then(r=>r.json()));
    expect(thirdNodes.map((n:{nodeId:string})=>n.nodeId)).not.toContain(endpoint.device);
    const denied=await third.page.evaluate(()=>fetch('/connected/api/v1/admin/pairing').then(r=>r.status));expect(denied).toBe(404);

    // Reproduce a computer that opens its own Hub and is also paired to another.
    await peer.getByRole('link',{name:'连接设置',exact:true}).click();
    await peer.getByLabel('局域网地址').selectOption('127.0.0.1');
    await peer.getByLabel('管理员口令',{exact:true}).fill(`fixture-${randomUUID()}`);
    await peer.getByText('高级设置：端口').click();
    await peer.getByLabel('工作台及接入').fill(String(await port()));
    await peer.getByLabel('设备连接',{exact:true}).fill(String(await port()));
    await peer.getByRole('button',{name:'开启主机'}).click();
    await peer.getByRole('button',{name:'生成连接码和密钥'}).click();
    const otherCodeField=peer.getByRole('textbox',{name:'发给另一台电脑的连接码'});
    await expect(otherCodeField).toHaveValue(/^DG2\./);const otherCode=await otherCodeField.inputValue();
    const otherKey=await peer.getByLabel('本次生成的连接密钥',{exact:true}).inputValue();
    expect(otherCode).not.toBe(code);
    const connectedExtra=join(base,'仅开放到已连接主机');await mkdir(connectedExtra);
    expect(await peer.evaluate(async folder=>(await fetch('/local/v1/add-folder',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify({folder,target:'connected'})})).status,connectedExtra)).toBe(200);
    const bindings=await peer.evaluate(()=>fetch('/local/v1/status').then(r=>r.json()));
    expect(bindings.localFolders.filter((f:{path:string})=>f.path===connectedExtra).map((f:{id:string})=>f.id.split('/')[0])).toEqual([endpoint.device]);
    await expect.poll(()=>peer.evaluate(()=>fetch('/api/v1/nodes').then(r=>r.json()).then(nodes=>nodes.some((n:{nodeId:string;online:boolean})=>n.nodeId==='dockroom-host'&&n.online)))).toBe(true);
    const ownRevocation=await peer.evaluate(async()=>{
      const u=await fetch('/api/v1/me').then(r=>r.json());
      return (await fetch('/api/v1/nodes/dockroom-host/revoke',{method:'POST',headers:{'X-CSRF-Token':u.csrf}})).status;
    });expect(ownRevocation).toBe(204);
    await peer.getByRole('link',{name:'打开本机双栏',exact:true}).click();
    await expect(peer.getByRole('heading',{name:'正在等待共享文件夹'})).toBeVisible();
    await peer.getByRole('link',{name:'连接设置',exact:true}).click();
    await peer.getByRole('link',{name:'打开已连接主机双栏',exact:true}).click();
    await expect(peer).toHaveURL(/\/computer\/$/);await peer.goto(new URL('/connected/',peer.url()).href);
    const original=await peer.evaluate(()=>fetch('/connected/api/v1/me').then(r=>r.json()));expect(original.localNode).toBe(endpoint.device);
    await expect(peer.getByRole('article',{name:'左栏目录'}).getByText('接入验证.txt',{exact:true})).toBeVisible();
    await peer.getByRole('link',{name:'连接设置',exact:true}).click();
    await peer.getByRole('button',{name:'重新连接',exact:true}).click();
    await expect(peer.getByRole('link',{name:'打开已连接主机双栏',exact:true})).toBeVisible();
    const same=await peer.evaluate(()=>fetch('/local/v1/status').then(r=>r.json()));expect(same.device).toBe(endpoint.device);
    // Failed replacement does not erase the existing connection; a successful
    // replacement preserves the selected path and file while changing identity.
    await third.page.getByRole('link',{name:'连接设置',exact:true}).click();
    await third.page.getByRole('button',{name:'更换主机',exact:true}).click();
    await third.page.getByRole('textbox',{name:'主机连接码'}).fill(otherCode);
    await third.page.getByLabel('连接密钥',{exact:true}).fill(wrongKey);
    await third.page.getByRole('button',{name:'确认更换主机'}).click();
    await expect(third.page.getByText(/连接密钥错误/)).toBeVisible();
    const beforeSwap=await third.page.evaluate(()=>fetch('/local/v1/status').then(r=>r.json()));
    expect(thirdNodes.some((n:{nodeId:string})=>n.nodeId===beforeSwap.device)).toBe(true);
    await writeFile(join(beforeSwap.folder,'换主机保留.txt'),'keep this fixture');
    await third.page.getByLabel('连接密钥',{exact:true}).fill(otherKey);
    await third.page.getByRole('button',{name:'确认更换主机'}).click();
    await third.page.waitForURL('**/computer/');await third.page.goto(new URL('/connected/',third.page.url()).href);
    const afterSwap=await third.page.evaluate(()=>fetch('/local/v1/status').then(r=>r.json()));
    expect(afterSwap.hub).not.toBe(beforeSwap.hub);expect(afterSwap.device).not.toBe(beforeSwap.device);expect(afterSwap.folder).toBe(beforeSwap.folder);
    await expect(third.page.getByRole('article',{name:'左栏目录'}).getByText('换主机保留.txt',{exact:true})).toBeVisible();

    await stop(peerProcess);
    const restarted=await launch('peer',true);await expect(restarted.page.getByRole('heading',{name:'磁盘文件传输'})).toBeVisible();
    const again=await restarted.page.evaluate(()=>fetch('/local/v1/status').then(r=>r.json()));expect(again.device).toBe(endpoint.device);
    // Grant revocation must survive a WebSocket reconnect.
    const revoke=await host.evaluate(async ({nodeId})=>{
      const u=await fetch('/api/v1/me').then(r=>r.json());return (await fetch('/api/v1/users/admin/grants',{method:'PUT',headers:{'Content-Type':'application/json','X-CSRF-Token':u.csrf},body:JSON.stringify({nodeId,rootId:'files',read:false})})).status;
    },{nodeId:endpoint.device});expect(revoke).toBe(204);
    await stop(restarted.p);
    const reconnected=await launch('peer',true);await expect(reconnected.page.getByRole('heading',{name:'磁盘文件传输'})).toBeVisible();
    const visibleRoots=await host.evaluate(async id=>(await fetch(`/api/v1/nodes/${id}/roots`)).json(),endpoint.device);
    expect(visibleRoots.map((r:{rootId:string})=>r.rootId)).toEqual([bindings.localFolders.find((f:{path:string})=>f.path===connectedExtra).id.split('/')[1]]);
    host.once('dialog',d=>d.accept());await host.getByRole('button',{name:'停用新设备接入'}).click();
    const fourth=await launch('fourth');await enter(fourth.page,code,newKey);await expect(fourth.page.getByText(/连接密钥错误/)).toBeVisible();
    await host.screenshot({path:join(artifacts,'windows-pairing-online.png'),fullPage:true});
  }finally{
    await browser.close();
    await Promise.all(processes.map(stop));
  }
});
