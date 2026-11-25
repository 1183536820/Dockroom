const $=id=>document.getElementById(id);
let busy=false,folder='',last,listed='',enterWhenReady=false,replacing=false,polling=false;
const report=(msg,kind='')=>{$('notice').textContent=msg;$('notice').className=kind;$('notice').hidden=!msg;};
async function api(path,method='GET',data,headers={}){
  const response=await fetch(`/local/v1/${path}`,{method,cache:'no-store',headers:{...(data?{'Content-Type':'application/json'}:{}),...headers},body:data&&JSON.stringify(data)});
  if(response.status===204)return;
  const payload=await response.json();if(!response.ok)throw new Error(payload.message||'操作失败');return payload;
}
async function refresh(initial=false){
  const s=await api('status');last=s;
  if(initial){for(const address of s.addresses){const option=new Option(address==='127.0.0.1'?'仅本机（测试）':address,address);$('address').add(option);}report(s.error||'',s.error?'error':'');}
  const existed=!!s.host.address;
  $('host-form').hidden=s.hostRunning;$('host-live').hidden=!s.hostRunning;$('host-settings').hidden=existed;
  $('address').required=!existed;$('password').required=!existed;
  $('start-host').textContent=existed?'启动已有主机':'开启主机';
  if(s.hostRunning)$('host-address').textContent=s.host.address==='127.0.0.1'?'仅本机测试，其他电脑无法接入':`${s.host.address} · ${s.host.browserPort} / ${s.host.devicePort}`;
  renderJoin(s);
  $('workbench-entry').hidden=!s.hostRunning;
  $('remote-entry').hidden=!s.remoteAvailable||s.agentStatus!=='online';
  if(s.device){
    $('pair-tag-row').hidden=!!s.reusablePairing;$('pair-tag').textContent=s.tag;$('connected-address').textContent=s.hub;
    let state=s.agentStatus;
    if(state==='online'&&s.remoteAvailable)state='ready';
    $('agent-status').textContent={connecting:'正在连接主机…',approval:'旧版接入：请在主机批准设备。',online:s.reusablePairing?'设备已连通，但工作台未就绪。':'已连接。请在主机电脑使用双栏。',ready:'已连接，可打开磁盘双栏收发文件。','one-device':'已连接，目前只有一台电脑的共享文件夹可用。','no-folders':'设备已连通，但没有可用的共享文件夹。',unavailable:'设备已连通，但已连接主机的工作台暂不可用。',denied:'主机拒绝了此设备，可重新连接或更换主机。',offline:'主机暂时离线，正在重连。',error:'本机设备无法启动，请检查目录或更换主机。'}[state]||'正在连接主机…';
    $('agent-status').className=state==='ready'?'success':'';
    if(state==='offline'&&s.control)$('agent-status').textContent=`设备通道未连接，正在重试 ${new URL(s.control).host}。请确认主机仍在运行，并允许可信局域网/VPN 访问这个端口。`;
    if(enterWhenReady&&!replacing&&s.agentStatus==='online'&&s.remoteAvailable)location.assign('/computer/');
  }
  $('folders').hidden=!new URLSearchParams(location.search).has('folders')||!s.localFolders.length;$('add-folder').hidden=!s.hostRunning&&!s.reusablePairing;
  const target=$('folder-target');
  if(initial)target.value=new URLSearchParams(location.search).get('folders')==='connected'?'connected':'host';
  if(!s.hostRunning)target.value='connected';else if(!s.reusablePairing)target.value='host';
  $('folder-target-row').hidden=!s.hostRunning||!s.reusablePairing;
  const folders=s.localFolders.filter(f=>f.id.startsWith('dockroom-host/')===(target.value==='host'));
  const marker=JSON.stringify(folders);
  if(listed!==marker){listed=marker;$('folder-list').replaceChildren();for(const f of folders){
    const li=document.createElement('li');const name=document.createElement('span');name.className='mono';name.textContent=`${f.path} · ${f.mode==='readWrite'?'可收发':'只读'}`;
    const open=document.createElement('button');open.type='button';open.textContent='在资源管理器打开';open.onclick=()=>void perform(li,()=>api('open-folder','POST',{id:f.id}));li.append(name,open);$('folder-list').append(li);
  }}
}
function renderJoin(s){
  $('join-form').hidden=!!s.device&&!replacing;$('agent-live').hidden=!s.device;
  $('folder-choice').hidden=true;$('replace-note').hidden=!replacing;$('cancel-replace').hidden=!replacing;
  $('join-submit').textContent=replacing?'确认更换主机':'连接主机';
}
async function perform(form,action){
  if(busy)return;busy=true;
  const buttons=[...form.querySelectorAll('button')];buttons.forEach(b=>b.disabled=true);report('正在处理…');
  try{await action();await refresh();report('');}catch(e){report(e.message,'error');}
  finally{busy=false;buttons.forEach(b=>b.disabled=false);}
}
try{
  const hash=location.hash.slice(1);
  if(/^[A-Za-z0-9_-]{43}$/.test(hash)){history.replaceState(null,'','/setup');await api('open','POST',undefined,{'X-Local-Key':hash});enterWhenReady=true;}
  await refresh(true);$('cards').hidden=false;$('exit').hidden=false;
  if(hash==='workbench-unavailable')report('主机工作台还未就绪。请确认两台电脑都已升级、主机在线，再点「打开双栏工作台」。','error');
  if(hash==='join'&&!$('join-form').hidden)$('invite').focus();
  setInterval(async()=>{if(busy||polling)return;polling=true;try{await refresh();}catch{report('无法读取连接状态，请检查渡柜程序是否仍在运行。','error');}finally{polling=false;}},3000);
}catch(e){report(e.message,'error');}
$('host-form').onsubmit=e=>{e.preventDefault();void perform(e.currentTarget,async()=>{
  const saved=!!last?.host.address, password=$('password').value;
  await api('host','POST',saved?{}:{address:$('address').value,browserPort:Number($('browser-port').value),devicePort:Number($('device-port').value),password:$('password').value});
  $('password').value='';
  if(!saved)await signIn(password);
});};
async function signIn(password){
  const response=await fetch('/api/v1/session',{method:'POST',cache:'no-store',headers:{'Content-Type':'application/json'},body:JSON.stringify({user:'admin',password})});
  if(!response.ok)throw new Error('管理员口令无效或尝试过于频繁');
}
async function pairing(action){
  const me=await fetch('/api/v1/me',{cache:'no-store'});
  if(me.status===401){$('host-auth').hidden=false;$('unlock').focus();return;}
  if(!me.ok)throw new Error('无法读取主机登录状态');
  const user=await me.json();if(!user.admin)throw new Error('请通过工作台登录管理员账号');
  const url='/api/v1/admin/pairing';
  const current=await fetch(url,{cache:'no-store'});
  if(!current.ok)throw new Error('无法读取连接设置');
  const status=await current.json();
  let result={code:status.code,key:status.active?$('host-key').value:'',active:status.active};
  if(action||!status.active||!status.disk){
    const response=await fetch(url,{method:'POST',headers:{'Content-Type':'application/json','X-CSRF-Token':user.csrf},body:JSON.stringify({action:action||(status.active?'rotate':'enable'),disk:true})});
    if(!response.ok)throw new Error('连接设置未能更新，请重新查看状态');
    result=response.status===204?{code:status.code,key:'',active:false}:{...await response.json(),active:true};
  }
  $('host-code').value=result.code;$('share').hidden=false;
  $('host-key').value=result.key;$('host-key').type='password';$('show-key').textContent='显示密钥';$('key-display').hidden=!result.key;
  $('rotate').hidden=!result.active;$('disable').hidden=!result.active;
  $('key-note').textContent=result.key?'使用此密钥接入的设备可浏览和收发本机磁盘文件。只发给你自己的电脑。':result.active?'此主机已启用免审批。使用原密钥；如遗失，可更换密钥。':'已停用新设备接入；已连接的设备不受影响。';
}
$('generate').onclick=()=>void perform($('host-live'),()=>pairing());
$('host-auth').onsubmit=e=>{e.preventDefault();void perform(e.currentTarget,async()=>{await signIn($('unlock').value);$('unlock').value='';$('host-auth').hidden=true;await pairing();});};
async function copyField(id,label){try{await navigator.clipboard.writeText($(id).value);report(`${label}已复制`);}catch{report('无法复制，请手动选中后按 Ctrl+C');}}
$('copy-code').onclick=()=>void copyField('host-code','连接码');
$('copy-key').onclick=()=>void copyField('host-key','密钥');
$('show-key').onclick=()=>{const hidden=$('host-key').type==='password';$('host-key').type=hidden?'text':'password';$('show-key').textContent=hidden?'隐藏密钥':'显示密钥';};
$('rotate').onclick=()=>{if(confirm('更换后，旧密钥不能再接入新设备。已连接设备不受影响。继续？'))void perform($('host-live'),()=>pairing('rotate'));};
$('disable').onclick=()=>{if(confirm('停用后，将不再允许使用此密钥接入新设备。继续？'))void perform($('host-live'),()=>pairing('disable'));};
$('invite').oninput=()=>{const legacy=$('invite').value.trim().startsWith('DG1.');$('join-key-row').hidden=legacy;$('join-key').required=!legacy;};
$('join-form').onchange=()=>{$('folder-row').hidden=!$('join-form').querySelector('[value=existing]').checked;};
$('pick').onclick=()=>void perform($('join-form'),async()=>{const r=await api('pick','POST');if(r.folder){folder=r.folder;$('folder').textContent=folder;}});
$('join-form').onsubmit=e=>{e.preventDefault();void perform(e.currentTarget,async()=>{
  const newFolder=$('join-form').querySelector('[value=new]').checked;
  if(!replacing&&!newFolder&&!folder)throw new Error('请先选择一个文件夹');
  await api('join','POST',{code:$('invite').value.trim(),key:$('join-key').value.trim(),newFolder,folder,replace:replacing});$('invite').value='';$('join-key').value='';replacing=false;enterWhenReady=true;
});};
$('reconnect').onclick=()=>void perform($('agent-live'),async()=>{await api('reconnect','POST');enterWhenReady=false;});
$('replace').onclick=()=>{if(busy)return;replacing=true;enterWhenReady=false;renderJoin(last);$('invite').focus();};
$('cancel-replace').onclick=()=>{if(busy)return;replacing=false;$('invite').value='';$('join-key').value='';$('invite').dispatchEvent(new Event('input'));renderJoin(last);};
$('folder-target').onchange=()=>void refresh().catch(e=>report(e.message,'error'));
$('add-folder').onclick=()=>void perform($('folders'),async()=>{const target=$('folder-target').value;const r=await api('pick','POST');if(r.folder)await api('add-folder','POST',{folder:r.folder,target});});
$('exit').onclick=async()=>{if(busy||!confirm('退出渡柜？本机主机和设备连接会停止。'))return;try{await api('exit','POST');$('cards').hidden=true;$('exit').hidden=true;report('渡柜已退出。');}catch(e){report(e.message,'error');}};
