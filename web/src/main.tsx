import {StrictMode, useCallback, useRef, useEffect, useState} from 'react';
import type {FormEvent} from 'react';
import {createRoot} from 'react-dom/client';
import {QueryClient, QueryClientProvider, useQuery} from '@tanstack/react-query';
import {ApiError, call, connectedView} from './api';
import type {Contract} from './api';
import {Pane} from './pane';
import {Catalog} from './catalog';
import {Devices} from './devices';
import {activeJobStates,CopyConfirm,jobLabels,Tasks} from './copy';
import type {Clipboard} from './copy';
import {workbenchShortcut} from './shortcuts';
import './style.css';

const client = new QueryClient({defaultOptions: {queries: {retry: false, gcTime: 0}}});
type User = Contract['Identity'];
const localSetup = location.protocol==='http:'&&/^dockroom-[a-f0-9]+\.localhost$/.test(location.hostname);

function Login({signedIn}: {signedIn: (u: User) => void}) {
  const [error,setError] = useState(''); const [busy,setBusy] = useState(false);
  async function submit(e: FormEvent<HTMLFormElement>) {
    e.preventDefault(); const data = new FormData(e.currentTarget); setBusy(true); setError('');
    try { signedIn(await call<User>('/session', {method:'POST', body:{user:data.get('user'),password:data.get('password')}})); }
    catch (e) { setError((e as Error).message); } finally {setBusy(false);}
  }
  return <main className="signin"><div className="kicker">DOCKROOM / SIGN IN</div><h1>登录渡柜</h1><p className="muted">使用本机管理员分配的账号访问授权目录。</p><form onSubmit={submit}>
    <label>账号<input name="user" autoComplete="username" required /></label><label>口令<input name="password" type="password" autoComplete="current-password" required /></label>
    <button className="primary" disabled={busy}>{busy?'正在登录…':'登录'}</button>{error && <p role="alert" className="error">{error}</p>}
  </form></main>;
}
function Workbench({u, expire}: {u: User; expire: () => void}) {
  const [active,setActive] = useState(0); const [area,setArea] = useState<'files'|'devices'|'jobs'>('files');const [logError,setLogError] = useState('');
  const [clipboard,setClipboard]=useState<{current?:Clipboard; history:(Clipboard|undefined)[]}>({history:[]});
  const clip=clipboard.current;
  const [pending,setPending]=useState<Contract['CopyInput']>();const [locate,setLocate]=useState<{r:Contract['Ref'];i:number}>();
  const [recentId,setRecentId]=useState<string>();
  const [targets,setTargets]=useState<(Contract['Ref']|undefined)[]>([]);
  const reportTarget=useCallback((i:number,r?:Contract['Ref'])=>setTargets(old=>{
    if(JSON.stringify(old[i])===JSON.stringify(r))return old;const next=[...old];next[i]=r;return next;
  }),[]);
  const lock=useRef(false);const [sending,setSending]=useState(false);const [message,setMessage]=useState('');
  async function transfer(source:Contract['CopySource'],destination:Contract['Ref']){
    if(lock.current)return;lock.current=true;setSending(true);setMessage('正在提交传输…');
    try{
      const plan=await call<Contract['CopyPlan']>('/plans',{method:'POST',csrf:u.csrf,body:{action:'copy',sources:[source],destination,conflictPolicy:'skip'}});
      if(plan.target.state==='exists'){setMessage('目标目录已有同名文件，未覆盖。');return;}
      const job=await call<{jobId:string}>('/jobs',{method:'POST',csrf:u.csrf,idempotency:crypto.randomUUID(),body:{planId:plan.planId}});
      setRecentId(job.jobId);setMessage('');
    }catch(e){setMessage((e as Error).message);}finally{lock.current=false;setSending(false);}
  }
  function changeClip(next?:Clipboard){setClipboard(c=>({current:next,history:[...c.history.slice(-9),c.current]}));}
  function undoClip(){setClipboard(c=>c.history.length?{current:c.history.at(-1),history:c.history.slice(0,-1)}:c);}
  const recent=useQuery({queryKey:['copy-job',u.user,recentId],enabled:!!recentId,queryFn:({signal})=>call<Contract['CopyJob']>(`/jobs/${recentId}`,{signal}),refetchInterval:q=>q.state.error?false:!q.state.data||activeJobStates.has(q.state.data.state)?1000:false});
  const nodes = useQuery({queryKey:['nodes',u.user],queryFn:({signal})=>call<Contract['Node'][]>('/nodes',{signal}),refetchInterval:3000,refetchIntervalInBackground:true});
  const localNode=u.localNode??(localSetup&&!connectedView?'dockroom-host':undefined);
  const shownNodes=nodes.data?(localNode?[...nodes.data.filter(n=>n.nodeId===localNode),...nodes.data.filter(n=>n.nodeId!==localNode).sort((a,b)=>Number(b.online)-Number(a.online))]:nodes.data):[];
  useEffect(()=>{if(nodes.error instanceof ApiError && nodes.error.status===401)expire();},[nodes.error,expire]);
  async function logout(){ try {await call('/session',{method:'DELETE',csrf:u.csrf});expire();} catch(e) {if(e instanceof ApiError&&e.status===401){expire();return;}setLogError((e as Error).message);} }
  return <>
    <header><div className="brand"><span className="mark" aria-hidden="true"/><b>渡柜</b><span className="kicker wordmark">DOCKROOM</span></div><nav aria-label="主导航"><button aria-current={area==='files'?'page':undefined} onClick={()=>{setLocate(undefined);setArea('files');}}>工作台</button><button aria-current={area==='jobs'?'page':undefined} onClick={()=>setArea('jobs')}>任务记录</button>{u.admin&&<button aria-current={area==='devices'?'page':undefined} onClick={()=>setArea('devices')}>设备管理</button>}</nav><div className="account">{connectedView?<span>已连接主机</span>:<><span className="mono">{u.user}</span><button onClick={logout}>退出</button></>}</div></header>
    {logError&&<p role="alert" className="main-error">{logError}</p>}
    <main onKeyDown={e=>{if(area==='files'&&clipboard.history.length&&workbenchShortcut(e)==='z'){e.preventDefault();undoClip();}}}>
      <div className="intro"><div><div className="kicker">OPERATIONS / {area==='files'?'FILES':area==='jobs'?'JOBS':'DEVICES'}{localSetup?(connectedView?' · 已连接主机':' · 本机主机'):''}</div><h1>{area==='files'?'文件工作台':area==='jobs'?'任务记录':'设备管理'}</h1></div>{localSetup?<div className="quick-links"><a href="/computer/">浏览电脑磁盘</a><a href={connectedView?"/setup?folders=connected#folders":"/setup?folders=host#folders"}>添加本机文件夹</a><a href="/setup">连接设置</a></div>:<span className="stage">M2 · Windows 单文件</span>}</div>
      {area==='devices'?<Devices user={u}/>:area==='jobs'?<Tasks user={u} locate={r=>{setLocate({r,i:localSetup?(r.nodeId===localNode?0:1):active});setArea('files');}}/>:<>
        {!localSetup&&<div className="shelf"><span className="kicker">WORKSPACE</span><strong>剪贴板</strong>{clip?<><span className="clip-name" title={clip.native}>{clip.source.ref.nodeId} · {clip.name}</span><button onClick={()=>changeClip()}>清除</button></>:<span>选择一个 Windows 文件可复制</span>}</div>}{message&&<p className="banner" role="status">{message}</p>}
        {recentId&&<div className="copy-status"><span role="status">{recent.error?'暂时无法读取最新任务状态':recent.data?`最近复制：${jobLabels[recent.data.state]}`:'复制已提交，正在后台处理'}</span><button onClick={()=>setArea('jobs')}>查看任务记录</button></div>}
        {nodes.error?<p className="error" role="alert">未能更新设备状态：{nodes.error.message} <button onClick={()=>nodes.refetch()}>重试</button></p>:nodes.isPending?<p className="banner" role="status">正在读取电脑和共享文件夹…</p>:shownNodes.length===0?<section className="banner"><h2>正在等待共享文件夹</h2><p>确认两台电脑的渡柜程序仍在运行。另一台连接后，选中的文件夹会显示在双栏里。</p>{localSetup&&<a href="/setup">检查连接</a>}{!localSetup&&u.admin&&<button className="primary" onClick={()=>setArea('devices')}>连接设置</button>}</section>:<>
          <div className="pane-tabs" role="group" aria-label="切换面板"><button aria-pressed={active===0} onClick={()=>setActive(0)}>左栏目录</button><button aria-pressed={active===1} onClick={()=>setActive(1)}>右栏目录</button></div>
          {localNode&&<p className="file-guide">打开要接收文件的目录，在另一侧选中文件，点发送或下载。同名文件不会覆盖。</p>}
          <section className="panes" aria-label="双栏目录">{[0,1].map(i=>localNode&&!shownNodes.some(n=>(n.nodeId===localNode)===(i===0))?<article key={i} className={`pane ${active===i?"is-active":""}`} aria-label={i?"右栏目录":"左栏目录"}><div className="panel-state" role="status"><b>{i?"等待对方电脑共享文件夹":"本机没有可见的共享文件夹"}</b><span>确认对应电脑已连接且已授权文件夹，就绪后此处自动显示。</span><a href="/setup">检查连接</a></div></article>:<Pane key={i} index={i} user={u} nodes={localSetup?shownNodes.filter(n=>(n.nodeId===localNode)===(i===0)):shownNodes} direct={localSetup} destination={targets[1-i]} reportTarget={reportTarget} transfer={transfer} busy={sending||!!(recentId&&(!recent.data&&!recent.error||recent.data&&activeJobStates.has(recent.data.state)))} localNode={localNode} active={active===i} activate={()=>setActive(i)} clip={clip} copy={changeClip} completed={recent.data} locate={locate?.i===i?locate.r:undefined} paste={destination=>clip&&setPending({action:'copy',sources:[clip.source],destination,conflictPolicy:'skip'})}/>)}</section>
        </>}
        <footer>渡柜 / 文件工作台 <span>设备通过独立的加密连接读取本机目录</span></footer>
      </>}
    </main>
    {pending&&<CopyConfirm input={pending} user={u} close={()=>setPending(undefined)} done={id=>{setRecentId(id);setPending(undefined);}}/>}
  </>;
}
function App() {
  const [u,setU] = useState<User|null|undefined>(undefined); const [error,setError] = useState('');
  function expire(){client.clear();setU(null);}
  function load(signal?: AbortSignal){setError('');call<User>('/me',{signal}).then(setU).catch(e=>{if(e instanceof ApiError && e.status===401)setU(null);else if(!signal?.aborted)setError(e.message);});}
  useEffect(()=>{const ctl=new AbortController();load(ctl.signal);return ()=>ctl.abort();},[]);
  if (u===undefined) return <main className="banner" role="status">{error?<>{error} <button onClick={()=>load()}>重试</button>{connectedView&&<> <a href="/setup">返回连接设置</a></>}</>:'正在验证会话…'}</main>;
  if(u===null&&connectedView)return <main className="banner"><h1>已连接主机的会话暂不可用</h1><p><button onClick={()=>{setU(undefined);load();}}>再试一次</button> <a href="/setup">返回连接设置</a></p></main>;
  return u?<Workbench u={u} expire={expire}/>:<><header><div className="brand"><span className="mark" aria-hidden="true"/><b>渡柜</b><span className="kicker wordmark">DOCKROOM</span></div></header><Login signedIn={setU}/></>;
}
createRoot(document.getElementById('root')!).render(<StrictMode><QueryClientProvider client={client}>{location.pathname==='/computer/'?<Catalog/>:<App/>}</QueryClientProvider></StrictMode>);
