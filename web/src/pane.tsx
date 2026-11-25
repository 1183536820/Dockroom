import {useEffect, useMemo, useLayoutEffect, useRef, useState} from 'react';
import type {FormEvent, KeyboardEvent} from 'react';
import {useInfiniteQuery, useQuery} from '@tanstack/react-query';
import {getCoreRowModel, useReactTable} from '@tanstack/react-table';
import {ApiError,call} from './api';
import type {Contract} from './api';
import type {Clipboard} from './copy';
import {workbenchShortcut} from './shortcuts';

type Ref = Contract['Ref'];
type Position = {ref: Ref; native: string};
type History = {visited: Position[]; at: number};
const eq=(a?:Ref,b?:Ref)=>!!a&&!!b&&a.nodeId===b.nodeId&&a.rootId===b.rootId&&a.parts.length===b.parts.length&&a.parts.every((p,i)=>p===b.parts[i]);
const fmtDate = (x: string) => new Intl.DateTimeFormat('zh-CN',{month:'2-digit',day:'2-digit',hour:'2-digit',minute:'2-digit',hour12:false}).format(new Date(x));
const fmtSize = (n: number) => n < 1024 ? `${n} B` : n < 1048576 ? `${(n/1024).toFixed(1)} KiB` : n < 1073741824 ? `${(n/1048576).toFixed(1)} MiB` : `${(n/1073741824).toFixed(1)} GiB`;
function remembered(key:string):Position|undefined{
  try{const x=JSON.parse(localStorage.getItem(key)??'null'); const ref=x?.ref;if(typeof ref?.nodeId==='string'&&typeof ref.rootId==='string'&&Array.isArray(ref.parts)&&ref.parts.every((p:unknown)=>typeof p==='string'))return {ref,native:''};}catch{/* optional storage */}
}
export function Pane({index,user,nodes,active,activate,clip,copy,paste,locate,completed,localNode,direct,destination,reportTarget,transfer,busy}: {direct:boolean;destination?:Ref;reportTarget:(i:number,r?:Ref)=>void;transfer:(s:Contract["CopySource"],r:Ref)=>void;busy:boolean;index:number; user:Contract['Identity']; nodes:Contract['Node'][]; active:boolean; activate:()=>void;clip?:Clipboard;copy:(c:Clipboard)=>void;paste:(r:Ref)=>void;locate?:Ref;completed?:Contract['CopyJob'];localNode?:string}){
  const key=`dockroom.files.${location.pathname}.${localNode??"web"}.${user.user}.${index}`;
  const [history,setHistory]=useState<History>(()=>{const r=remembered(key);return {visited:r?[r]:[],at:0};});
  const [override,setOverride]=useState<string>();const [text,setText]=useState('');const [filter,setFilter]=useState('');const [serial,setSerial]=useState(0);
  const [path,setPath]=useState('');const [resolveError,setResolveError]=useState('');const [resolving,setResolving]=useState(false);const resolvingCtl=useRef<AbortController|null>(null);
  const [notice,setNotice]=useState('');const [picked,setPicked]=useState<Set<string>>(new Set());const anchor=useRef<number|null>(null);
  const table=useRef<HTMLTableSectionElement>(null);
  const refreshedJob=useRef<string>(undefined);
  const pos=history.visited[history.at];const requested=override??pos?.ref.nodeId;
  const node=nodes.find(x=>x.nodeId===requested)??(localNode?nodes.find(n=>(n.nodeId===localNode)===(index===0)):undefined)??nodes[Math.min(index,nodes.length-1)];const nodeId=node.nodeId;
  const deviceName=(id:string)=>id===localNode?'本机':id==='dockroom-host'?'主机电脑':`电脑 · ${id}`;
  const roots=useQuery({queryKey:['roots',user.user,nodeId],queryFn:({signal})=>call<Contract['Root'][]>(`/nodes/${nodeId}/roots`,{signal}),refetchInterval:5000});
  const root=roots.data?.find(x=>pos?.ref.nodeId===nodeId&&x.rootId===pos.ref.rootId)??roots.data?.[0];
  const ref:Ref|undefined=root&&(pos?.ref.nodeId===nodeId&&pos.ref.rootId===root.rootId?pos.ref:{nodeId,rootId:root.rootId,parts:[]});
  const trustedRef=roots.isSuccess?ref:undefined;
  const listing=useInfiniteQuery({queryKey:['list',user.user,index,trustedRef,filter,serial,node.online],enabled:!!trustedRef&&node.online,
    initialPageParam:'',queryFn:({signal,pageParam})=>call<Contract['Listing']>('/fs/list',{method:'POST',csrf:user.csrf,body:{ref:trustedRef,filter,cursor:pageParam},signal}),getNextPageParam:p=>p.nextCursor||undefined});
  const live=node.online&&!roots.error&&!listing.error?listing.data?.pages[0]:undefined;
  const native=live?.nativePath??(trustedRef&&eq(trustedRef,pos?.ref)?pos?.native:'')??'';
  const viewKey=JSON.stringify(trustedRef);const rows=useMemo(()=>live?listing.data!.pages.flatMap(p=>p.entries):[],[live,listing.data]);
  const grid=useReactTable({data:rows,columns:[{accessorKey:'name'},{accessorKey:'sizeBytes'},{accessorKey:'mtimeUtc'}],getCoreRowModel:getCoreRowModel()});
  const selected=picked.size===1?rows.find(row=>picked.has(row.version)&&row.kind==='file'):undefined;
  const canCopy=!!(selected&&live&&trustedRef&&node.os==='windows');
  const canPaste=!!(live&&trustedRef&&clip&&root?.canWrite&&node.os==='windows');
  const writable=!!(direct&&live&&root?.canWrite&&node.os==='windows');
  useLayoutEffect(()=>{if(!direct)return;reportTarget(index,writable?trustedRef:undefined);return ()=>reportTarget(index);},[index,viewKey,writable,direct,reportTarget]);
  function send(){if(canCopy&&selected&&trustedRef&&destination&&!busy)transfer({ref:{...trustedRef,parts:[...trustedRef.parts,selected.name]},version:selected.version},destination);}
  function copyItem(){if(canCopy&&selected&&trustedRef)copy({source:{ref:{...trustedRef,parts:[...trustedRef.parts,selected.name]},version:selected.version},name:selected.name,native});}
  function pasteItem(){if(canPaste&&trustedRef)paste(trustedRef);}
  function shortcuts(e:KeyboardEvent){if(direct)return;const key=workbenchShortcut(e);
    if(key==='c'&&canCopy){e.preventDefault();copyItem();}if(key==='v'&&canPaste){e.preventDefault();pasteItem();}}
  function reset(){resolvingCtl.current?.abort();resolvingCtl.current=null;setResolving(false);setText('');setFilter('');setResolveError('');setNotice('');setPicked(new Set());anchor.current=null;}
  function go(next:Ref, name=''){reset();setOverride(undefined);setHistory(h=>eq(h.visited[h.at]?.ref,next)?h:{visited:[...h.visited.slice(0,h.at+1),{ref:next,native:name}].slice(-50),at:Math.min(49,h.at+1-(h.visited.length===0?1:0))});}
  useEffect(()=>{if(!live||!trustedRef)return;setHistory(h=>{
    const now=h.visited[h.at];if(!eq(now?.ref,trustedRef)){
      const next={ref:trustedRef,native:live.nativePath};const visited=[...h.visited.slice(0,h.at+1),next].slice(-50);return {visited,at:visited.length-1};
    };if(now.native===live.nativePath)return h;return {...h,visited:h.visited.map((v,i)=>i===h.at?{...v,native:live.nativePath}:v)};
  });try{localStorage.setItem(key,JSON.stringify({ref:trustedRef}));}catch{/* optional storage */}},[live,viewKey,key]);
  useEffect(()=>{setPath(native);},[native,viewKey]);
  useEffect(()=>{if(text===filter)return;const t=setTimeout(()=>{setFilter(text);setPicked(new Set());anchor.current=null;},180);return ()=>clearTimeout(t);},[text,filter]);
  useEffect(()=>()=>resolvingCtl.current?.abort(),[]);
  useEffect(()=>{if(locate)go(locate);},[locate]);
  useEffect(()=>{
    if(completed?.state==='succeeded'&&completed.jobId!==refreshedJob.current&&eq(trustedRef,completed.plan.input.destination)){
      refreshedJob.current=completed.jobId;setSerial(s=>s+1);
    }
  },[completed,viewKey]);
  async function jump(e:FormEvent){
    e.preventDefault();if(!trustedRef||!node.online||resolving)return;
    setResolveError('');setResolving(true);const ctl=new AbortController();resolvingCtl.current=ctl;
    try{const x=await call<Contract['Resolved']>('/fs/resolve',{method:'POST',csrf:user.csrf,body:{ref:trustedRef,input:path},signal:ctl.signal});if(!ctl.signal.aborted){go(x.ref,x.nativePath);setPath(x.nativePath);}}
    catch(e){if(!ctl.signal.aborted)setResolveError((e as Error).message);}
    finally{if(resolvingCtl.current===ctl){resolvingCtl.current=null;setResolving(false);}}
  }
  function toggle(row:Contract['Entry'],i:number,shift:boolean){if(row.kind==='unsupported')return;setPicked(prev=>{
    if(direct)return new Set(prev.has(row.version)?[]:[row.version]);const selected=new Set(prev);if(shift&&anchor.current!==null){rows.slice(Math.min(anchor.current,i),Math.max(anchor.current,i)+1).forEach(r=>{if(r.kind!=='unsupported')selected.add(r.version);});}
    else if(selected.has(row.version))selected.delete(row.version);else selected.add(row.version);return selected;
  });if(!shift)anchor.current=i;}
  function rowKeys(e:KeyboardEvent,i:number,row:Contract['Entry']){
    if((e.key==='ArrowUp'||e.key==='ArrowDown')&&e.target===e.currentTarget){e.preventDefault();const n=table.current?.rows[i+(e.key==='ArrowDown'?1:-1)];n?.focus();}
    if(e.target!==e.currentTarget)return;
    if(e.key==='Enter'&&row.kind==='directory'&&trustedRef){e.preventDefault();go({...trustedRef,parts:[...trustedRef.parts,row.name]});}
    if(e.key===' '){e.preventDefault();toggle(row,i,e.shiftKey);}
  }
  function step(delta:number){reset();setOverride(undefined);setHistory(h=>({...h,at:h.at+delta}));}
  return <article className={`pane ${active?'is-active':''}`} aria-label={index===0?'左栏目录':'右栏目录'} tabIndex={0} onFocus={activate} onKeyDown={shortcuts}
    onClick={e=>{if(!(e.target as Element).closest('button, input, select, label, tr, a')&&!window.getSelection()?.toString())e.currentTarget.focus({preventScroll:true});}}>
    <div className="pane-meta"><span className="kicker">{direct?(index?'对方电脑':'我的电脑'):'PANE / 0'+(index+1)}</span><span className={node.online?'state':'state offline'}>{node.online?'● 在线':'○ 离线'}{!node.online&&node.lastSeenUnix>0&&` · 上次在线 ${fmtDate(new Date(node.lastSeenUnix*1000).toISOString())}`}</span></div>
    {(!direct||nodes.length>1)&&<div className="endpoint"><label className="host"><span className="sr-only">设备</span><select aria-label="设备" value={nodeId} onChange={e=>{reset();setOverride(e.target.value);}}>{nodes.map(n=><option key={n.nodeId} value={n.nodeId}>{localNode?deviceName(n.nodeId):n.nodeId}</option>)}</select></label><span className="system">{node.os==='windows'?'Windows':node.os==='linux'?'Linux':'—'}</span></div>}
    <div className="root-line"><label>共享文件夹 <select aria-label="共享文件夹" disabled={!roots.data?.length} value={root?.rootId??''} onChange={e=>go({nodeId,rootId:e.target.value,parts:[]})}>{roots.data?.map(r=><option key={r.rootId} value={r.rootId}>{r.label}</option>)}</select></label><span className="mono badge">{root?.canWrite?'可粘贴':'只读'}</span></div>
    <form className="path-form" onSubmit={jump}><input className="mono" aria-label="原生绝对路径" placeholder="此处显示真实目录路径" disabled={!node.online||!trustedRef} value={path} onChange={e=>setPath(e.target.value)} /><button disabled={!node.online||!trustedRef||!path||resolving}>定位</button></form>
    <div className="native mono">{native||'—'}</div>
    {trustedRef&&<nav className="crumbs" aria-label="目录层级"><button onClick={()=>go({...trustedRef,parts:[]})}>{root?.label}</button>{trustedRef.parts.map((part,i)=><span key={i}> / <button aria-current={i===trustedRef.parts.length-1?'location':undefined} onClick={()=>go({...trustedRef,parts:trustedRef.parts.slice(0,i+1)})} title={part}>{part}</button></span>)}</nav>}
    {resolveError&&<p className="error" role="alert">{resolveError}</p>}
    {direct?<div className="copy-bar"><button className="primary" disabled={!canCopy||!destination||busy} onClick={send}>{index?"下载到本机":"发送到对方"}</button><small>{busy?"传输处理中…":!destination?"接收端未就绪，请打开可写目录":selected?selected.name:"请选择一个文件"}</small></div>:<div className="copy-bar"><button disabled={!canCopy} onClick={copyItem} aria-keyshortcuts="Control+C Meta+C" title="复制选中的文件（Ctrl+C）">复制文件</button><button className="primary" disabled={!canPaste} onClick={pasteItem} aria-keyshortcuts="Control+V Meta+V" title={!root?.canWrite?'目标只读':'粘贴到当前目录（Ctrl+V）'}>粘贴到此处</button></div>}
    <div className="tools"><div className="tool-group"><button aria-label="后退" title="后退" disabled={history.at===0} onClick={()=>step(-1)}>←</button><button aria-label="前进" title="前进" disabled={history.at+1>=history.visited.length} onClick={()=>step(1)}>→</button><button disabled={!trustedRef?.parts.length} onClick={()=>trustedRef&&go({...trustedRef,parts:trustedRef.parts.slice(0,-1)})}>上级</button><button disabled={!node.online} onClick={()=>{setNotice('');setSerial(s=>s+1);roots.refetch();}}>刷新</button><button disabled={!live} onClick={async()=>{try{await navigator.clipboard.writeText(live!.nativePath);setNotice('已复制当前目录路径');}catch{setNotice('浏览器无法复制；可选中并复制上方路径');}}}>复制路径</button></div><input aria-label="按名称筛选" placeholder="按名称筛选" value={text} disabled={!node.online||!trustedRef} onChange={e=>setText(e.target.value)} /></div>
    <div className="file-zone">
      {!node.online?<div className="panel-state" role="status"><b>设备离线</b><span>保留当前位置，重新连接后读取实时目录。</span></div>:roots.error?<div className="panel-state error" role="alert">{roots.error.message}</div>:roots.isSuccess&&!root?<div className="panel-state" role="status">这台电脑没有已授权的共享文件夹。请在对方电脑添加文件夹或检查授权。</div>:listing.error?<div className="panel-state error" role="alert"><span>{listing.error.message}</span>{listing.error instanceof ApiError&&listing.error.code==='LIST_EXPIRED'&&<button onClick={()=>setSerial(s=>s+1)}>重新读取</button>}</div>:!live?<div className="panel-state" role="status">正在读取目录…</div>:rows.length===0?<div className="panel-state" role="status"><b>{filter?'没有匹配项':'此目录为空'}</b><span className="mono">{live.nativePath}</span></div>:<table aria-label={`${nodeId} 文件`}><thead><tr><th aria-label="选择"/><th>名称</th><th>大小</th><th className="when">修改时间</th></tr></thead><tbody ref={table}>{grid.getRowModel().rows.map((x,i)=>{
        const row=x.original; const open=()=>trustedRef&&go({...trustedRef,parts:[...trustedRef.parts,row.name]});
        return <tr key={x.id} tabIndex={0} onKeyDown={e=>rowKeys(e,i,row)} onClick={()=>toggle(row,i,false)} onDoubleClick={()=>row.kind==='directory'&&open()} className={picked.has(row.version)?'selected':undefined}>
          <td><input type="checkbox" aria-label={`选择 ${row.name}`} disabled={row.kind==='unsupported'} checked={picked.has(row.version)} readOnly onClick={e=>{e.stopPropagation();toggle(row,i,e.shiftKey);}}/></td>
          <td title={row.name}><span className={`icon ${row.kind}`} aria-hidden="true"/>{row.kind==='directory'?<button className="name open" onClick={e=>{e.stopPropagation();open();}} aria-label={`打开 ${row.name}`}>{row.name}</button>:<span className="name">{row.name}{row.kind==='unsupported'?'（不支持）':''}</span>}</td>
          <td className="mono num">{row.kind==='file'?fmtSize(row.sizeBytes):'—'}</td><td className="mono num when" title={row.mtimeUtc}>{row.kind==='unsupported'?'—':fmtDate(row.mtimeUtc)}</td>
        </tr>;
      })}</tbody></table>}
    </div>
    <div className="pane-foot"><span>{live?`已载入 ${rows.length} 项 · 已选 ${picked.size} 项`:'尚未读取实时列表'}</span>{live&&listing.hasNextPage&&<button onClick={()=>listing.fetchNextPage()} disabled={listing.isFetchingNextPage}>{listing.isFetchingNextPage?'读取中…':'加载下一页'}</button>}<span role="status">{notice}</span></div>
  </article>;
}
