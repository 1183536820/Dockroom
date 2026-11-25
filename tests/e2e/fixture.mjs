import assert from 'node:assert/strict';
import {createHash,randomBytes,X509Certificate} from 'node:crypto';
import {execFileSync,spawn,spawnSync} from 'node:child_process';
import {copyFile,mkdir,mkdtemp,readFile,writeFile} from 'node:fs/promises';
import {request} from 'node:https';
import {createServer} from 'node:net';
import {join,resolve} from 'node:path';
import {fileURLToPath} from 'node:url';
import {setTimeout as wait} from 'node:timers/promises';

const repo=fileURLToPath(new URL('../../',import.meta.url));
const bin=join(repo,'.local','bin');
const exe=n=>n==='hub'&&process.env.DOCKROOM_FIXTURE_HUB_EXE?resolve(process.env.DOCKROOM_FIXTURE_HUB_EXE):join(bin,n+(process.platform==='win32'?'.exe':''));
const random=()=>randomBytes(24).toString('base64url');
const accounts={admin:random(),reader:random(),other:random()};
function command(name,args,input) {
  const r=spawnSync(exe(name),args,{cwd:repo,input:input===undefined?undefined:`${input}\n`,encoding:'utf8',timeout:20000,windowsHide:true});
  if(r.status!==0)throw new Error(`${name} ${args[0]} failed: ${r.stderr??r.error}`);
  return r.stdout.trim();
}
async function port(){
  const s=createServer();await new Promise((r,j)=>{s.once('error',j);s.listen(0,'127.0.0.1',r);});
  const p=s.address().port;await new Promise(r=>s.close(r));return p;
}
export async function until(fn,deadline=12000){
  const stop=Date.now()+deadline;let last;
  while(Date.now()<stop){try{const ret=await fn();if(ret)return ret;}catch(e){last=e;}await wait(150);}
  throw new Error(`timed out waiting for fixture${last?`: ${last.message}`:''}`);
}

export async function startFixture({write=false}={}){
  const sandbox=resolve(process.env.DOCKROOM_FIXTURE_BASE??join(repo,'.local'));await mkdir(sandbox,{recursive:true});
  const dir=await mkdtemp(join(sandbox,'m1-'));const hubDir=join(dir,'hub');
  const browserPort=await port();let devicePort=await port();while(devicePort===browserPort)devicePort=await port();
  const origin=`https://127.0.0.1:${browserPort}`;
  const nodes=['studio-07','archive-01']; const roots=['制作交付','归档卷']; const sub=['十一月','2026'];
  const nodeIDs=['out','archive'];
  const paths=roots.map(r=>join(dir,r));const data=nodes.map(n=>join(dir,n));
  const children=new Set();let hub, agents=[];
  function serve(name,args,env={}){
    const proc=spawn(exe(name),args,{cwd:repo,stdio:['ignore','pipe','pipe'],windowsHide:true,env:{...process.env,...env}});children.add(proc);
    proc.stdout.resume();proc.stderr.on('data',b=>{proc.lastError=((proc.lastError??'')+String(b)).slice(-12000);});
    proc.once('error',e=>{proc.lastError=e.message;});proc.once('exit',()=>children.delete(proc));return proc;
  }
  async function stop(proc){if(!proc||proc.exitCode!==null||proc.signalCode!==null)return;await new Promise(done=>{proc.once('exit',done);proc.kill();});}
  const startHub=()=>serve('hub',['serve','--data',hubDir,'--origin',origin,'--listen',`127.0.0.1:${browserPort}`,'--device-listen',`127.0.0.1:${devicePort}`]);
  try {
    command('hub',['init','--data',hubDir,'--host','127.0.0.1'],accounts.admin);
    for(const user of ['reader','other'])command('hub',['user-add','--data',hubDir,'--user',user],accounts[user]);
    const ca=await readFile(join(hubDir,'ca.pem'));
    const cert=new X509Certificate(await readFile(join(hubDir,'server.pem')));
    const spki=createHash('sha256').update(cert.publicKey.export({type:'spki',format:'der'})).digest('base64');
    function http(path,{method='GET',body,session,headers={},device=false}={}){return new Promise((ok,err)=>{
      const data=body===undefined?undefined:JSON.stringify(body);
      const req=request(new URL(path,device?`https://127.0.0.1:${devicePort}`:origin),{ca,method,headers:{Origin:origin,...(data===undefined?{}:{'Content-Type':'application/json','Content-Length':Buffer.byteLength(data)}),...(session?{Cookie:session.cookie,'X-CSRF-Token':session.csrf}:{}),...headers}},res=>{
        let text='';res.setEncoding('utf8');res.on('data',b=>text+=b);res.on('end',()=>{try{ok({status:res.statusCode,headers:res.headers,data:text&&res.headers['content-type']?.includes('json')?JSON.parse(text):text});}catch(e){err(e);}});
      });req.setTimeout(18000,()=>req.destroy(new Error('fixture HTTP timeout')));req.on('error',err);req.end(data);
    });}
    async function api(path,options){return http(`/api/v1${path}`,options);}
    const expect=async (promise,status)=>{const r=await promise;assert.equal(r.status,status,JSON.stringify(r.data));return r;};
    async function login(user){const r=await expect(api('/session',{method:'POST',body:{user,password:accounts[user]}}),200);return {cookie:r.headers['set-cookie'][0].split(';')[0],csrf:r.data.csrf};}
    hub=startHub();await until(async()=>{if(hub.lastError)throw new Error(hub.lastError);return (await http('/healthz')).status===204;});
    const admin=await login('admin');
    await expect(api('/nodes',{session:admin}),200);
    await assert.rejects(http('/device/control',{device:true}));
    const approvals=[];
    for(let i=0;i<nodes.length;i++){
      const root=paths[i];await mkdir(root);
      if(write){
        // Exact new fixture directories only; never vary ACLs outside this run.
        const current=execFileSync('whoami.exe',['/user','/fo','csv','/nh'],{windowsHide:true,encoding:'utf8'}).match(/S-1-5-21(?:-\d+){4}/)?.[0];
        assert.ok(current,'local test account SID');
        for(const p of [root,data[i]]){if(p===data[i])await mkdir(p);
          execFileSync('icacls.exe',[p,'/inheritance:r','/grant:r',`*${current}:(OI)(CI)F`,'*S-1-5-18:(OI)(CI)F','*S-1-5-32-544:(OI)(CI)F'],{windowsHide:true});
        }
      }else{await mkdir(data[i]);}
      await mkdir(join(root,sub[i],'空目录'),{recursive:true});
      const privateId=write?command('agent',['prepare-write','--path',root]):undefined;
      if(!write)await mkdir(join(root,'.dockroom'));
      await writeFile(join(root,'.dockroom','private sentinel.txt'),'hidden');
      await copyFile(join(hubDir,'ca.pem'),join(data[i],'ca.pem'));
      const identity=command('agent',['pin','--path',root]);
      const conf={nodeId:nodes[i],hub:origin,control:`wss://127.0.0.1:${devicePort}`,roots:[{rootId:nodeIDs[i],label:roots[i],mode:write||i===1?'readWrite':'readOnly',path:root,identity,privateId}]};
      await writeFile(join(data[i],'config.json'),JSON.stringify(conf));
      const r=await expect(api('/enrollments',{method:'POST',session:admin,body:{nodeId:nodes[i]}}),201);
      const fp=command('agent',['enroll','--data',data[i]],r.data.code);
      await expect(http('/device/enroll',{method:'POST',body:{nodeId:nodes[i],code:r.data.code,csr:'unused'}}),403);
      approvals.push(fp);
    }
    const outside=join(dir,'fixture-outside');await mkdir(outside);await writeFile(join(outside,'outside sentinel.txt'),'outside');
    if(process.platform==='win32')execFileSync('cmd.exe',['/c','mklink','/J',join(paths[0],'目录链接'),outside],{windowsHide:true});
    else {await (await import('node:fs/promises')).symlink(outside,join(paths[0],'目录链接'));}
    await writeFile(join(paths[0],sub[0],'封面 修订.tif'),'');await writeFile(join(paths[0],sub[0],'索引 清单.csv'),'x');await writeFile(join(paths[0],sub[0],'排版说明.pdf'),'synthetic sample');
    await mkdir(join(paths[0],sub[0],'印刷稿','交付版'),{recursive:true});
    await mkdir(join(paths[1],sub[1],'十月归档'),{recursive:true});await mkdir(join(paths[1],sub[1],'样张'),{recursive:true});await writeFile(join(paths[1],sub[1],'交接说明.txt'),'fixture');
    await mkdir(join(paths[0],'分页夹具'));await Promise.all(Array.from({length:207},(_,i)=>writeFile(join(paths[0],'分页夹具',`fixture-${String(i).padStart(3,'0')}.txt`), String(i))));
    agents=nodes.map((_,i)=>serve('agent',['serve','--data',data[i]]));
    const pending=(await expect(api('/admin/nodes',{session:admin}),200)).data;assert.equal(pending.length,2);assert.ok(pending.every(n=>n.state==='pending'&&!n.online));
    await expect(api(`/nodes/${nodes[0]}/approve`,{method:'POST',session:admin,body:{fingerprint:'wrong'}}),409);
    for(let i=0;i<nodes.length;i++)await expect(api(`/nodes/${nodes[i]}/approve`,{method:'POST',session:admin,body:{fingerprint:approvals[i]}}),204);
    await until(async()=> (await expect(api('/admin/nodes',{session:admin}),200)).data.filter(n=>n.online).length===2);
    const grant=(user,i,read,write=false)=>expect(api(`/users/${user}/grants`,{method:'PUT',session:admin,body:{nodeId:nodes[i],rootId:nodeIDs[i],read,write}}),204);
    await grant('reader',0,true,write);await grant('reader',1,true,write);await grant('other',0,true);
    return {dir,origin,spki,paths,nodes,sub,accounts,admin,api,login,expect,grant,ref:(i,parts=[])=>({nodeId:nodes[i],rootId:nodeIDs[i],parts}),
      stopAgent:async i=>stop(agents[i]), startAgent:(i,phase,marker)=>{agents[i]=serve(phase?'agent-fixture':'agent',['serve','--data',data[i]],phase?{DOCKROOM_FIXTURE_PAUSE:phase,DOCKROOM_FIXTURE_MARKER:marker}:{});},
      repinUnderSameID:async i=>{
        const changed=join(dir,'replacement-fixture');await mkdir(changed);await writeFile(join(changed,'replacement sentinel.txt'),'new root');
        const configPath=join(data[i],'config.json');const config=JSON.parse(await readFile(configPath,'utf8'));
        config.roots[0].path=changed; config.roots[0].identity=command('agent',['pin','--path',changed]);await writeFile(configPath,JSON.stringify(config));
      },
      restartHub:async()=>{await stop(hub);hub=startHub();await until(async()=> (await http('/healthz')).status===204);},
      pids:()=>[hub.pid,...agents.map(a=>a.pid)], close:async()=>{await Promise.all([...children].map(stop));}};
  }catch(e){await Promise.all([...children].map(stop));throw e;}
}
