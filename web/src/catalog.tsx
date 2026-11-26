import {useRef,useState} from 'react';
import {useQuery,useQueryClient} from '@tanstack/react-query';

type Listing={drives?:string[];entries?:{name:string;dir:boolean}[];more?:boolean};
type Session={remoteAvailable:boolean;hub:string;device:string;agentStatus:string};
type Position={path:string;offset:number;file?:string;directory?:boolean};
const home:Position={path:'',offset:0};
async function request<T>(url:string,opts:RequestInit={}):Promise<T>{
 const res=await fetch(url,{cache:'no-store',...opts});if(!res.ok){const e=await res.json().catch(()=>({}));throw new Error(e.code==='SERVER_LOGIN'?'此连接属于旧版。请在主机生成新的磁盘密钥，重新连接一次。':e.message??`请求失败（${res.status}）`)}return res.status===204?undefined as T:res.json();
}
function Browser({remote,pos,set,session,busy,other,copy,shortcut}:{shortcut:(key:string)=>void;remote:boolean;pos:Position;set:(p:Position)=>void;session?:Session;busy:boolean;other:Position;copy:()=>void}){
 const {path,offset,file}=pos,prefix=remote?'/connected/api/v1':'/local/v1';
 const query=useQuery({queryKey:['disk',prefix,remote?session?.device:'',path,offset],queryFn:({signal})=>request<Listing>(`${prefix}/catalog?`+new URLSearchParams({path,offset:String(offset)}),{signal}),retry:false});
 const open=(p:string)=>set({path:p,offset:0});
 return <article className="pane disk-pane" tabIndex={0} onClick={e=>{if(!(e.target as Element).closest('input,button,a'))e.currentTarget.focus()}} onKeyDown={e=>{
   const key=e.key.toLowerCase();if(!(e.ctrlKey||e.metaKey)||e.shiftKey||e.altKey||!['c','v'].includes(key)||(e.target as Element).closest('input,textarea,[contenteditable=true]')||window.getSelection()?.toString()||!query.isSuccess)return;
   e.preventDefault();if(!busy&&!e.repeat)shortcut(key);
 }}><h2>{remote?'远程服务器':'我的电脑'}</h2><div className="tools"><button disabled={!path||busy} onClick={()=>open(path.length===3?'':path.slice(0,Math.max(3,path.lastIndexOf('\\'))))}>上级</button><button disabled={busy} onClick={()=>query.refetch()}>刷新</button><span className="mono">{path||'磁盘'}</span></div>
 <form key={path} className="path-form" onSubmit={e=>{e.preventDefault();const p=String(new FormData(e.currentTarget).get('path')??'').trim().replace(/\//g,'\\').replace(/^([a-z]):/i,(_,d:string)=>d.toUpperCase()+':').replace(/\\+$/,'');open(/^[A-Z]:$/.test(p)?p+'\\':p)}}><input name="path" aria-label="文件夹路径" defaultValue={path} placeholder="也可以粘贴文件夹路径"/><button disabled={busy}>打开目录</button></form>
 <div className="copy-bar"><button className="primary" disabled={!file||busy||!other.path||!session||!query.isSuccess} onClick={copy}>{remote?'下载到本机':'发送到对方'}</button><small>{file??'请选择文件'}{other.path&&` → ${other.path}`}</small></div>
 {query.error?<p role="alert">{query.error.message}</p>:!query.data?<p>正在读取…</p>:<><ul>{query.data.drives?.map(d=><li key={d}><button disabled={busy} onClick={()=>open(d)}>{d}</button></li>)}{query.data.entries?.map(v=><li key={v.name}>{v.dir?<button disabled={busy} aria-pressed={file===v.name} onClick={()=>set({...pos,file:v.name,directory:true})} onDoubleClick={()=>open(path.replace(/\\$/,'')+'\\'+v.name)}>{file===v.name?"✓ ":""}📁 {v.name}</button>:<button disabled={busy} aria-pressed={file===v.name} onClick={()=>set({...pos,file:v.name,directory:false})}>{file===v.name?'✓ ':''}{v.name}</button>}</li>)}</ul>{!(query.data.drives??query.data.entries)?.length&&<p>没有可显示的项目</p>}{(offset>0||query.data.more)&&<div><button disabled={busy||!offset} onClick={()=>set({path,offset:offset-200})}>上一页</button><button disabled={busy||!query.data.more} onClick={()=>set({path,offset:offset+200})}>下一页</button></div>}</>}</article>
}
function Explorer({session}:{session?:Session}){
 const [local,L]=useState(home),[remote,R]=useState(home),[msg,M]=useState(''),[busy,B]=useState(false);const lock=useRef(false);const client=useQueryClient();
 const clip=useRef<{download:boolean;source:Position}|undefined>(undefined);
 function shortcut(side:boolean,key:string){
   if(key==='c'){const source=side?remote:local;if(source.file){clip.current={download:side,source:{...source}};M(`已复制 ${source.file}，请在另一栏按 Ctrl+V`)}return}
   if(!clip.current||clip.current.download===side){M('请先在另一栏选中项目并按 Ctrl+C');return}
   void copy(clip.current.download,clip.current.source);
 }
 async function copy(download:boolean,saved?:Position){
   if(lock.current||!session)return;const src=saved??(download?remote:local),dst=download?local:remote;
   if(!src.file||!dst.path){M('请先打开接收目录');return}lock.current=true;B(true);let done=0;
   const child=(p:string,n:string)=>p.replace(/\\$/,'')+'\\'+n;
   const items=[{source:src.path,destination:dst.path,name:src.file,directory:!!src.directory}];
   try{
     M(`正在读取 ${src.file}…`);
     for(let i=0;i<items.length;i++){const item=items[i];if(!item.directory)continue;
       const source=child(item.source,item.name),destination=child(item.destination,item.name);
       for(let offset=0;;offset+=200){const data=await request<Listing>((download?'/connected/api/v1':'/local/v1')+'/catalog?'+new URLSearchParams({path:source,offset:String(offset)}));
         if(!data.entries)throw new Error('源目录不可用');for(const v of data.entries)items.push({source,destination,name:v.name,directory:v.dir});if(!data.more)break;
       }
     }
     for(const item of items){M(`正在复制 ${done+1}/${items.length} · ${item.name}`);await request('/local/v1/disk-transfer',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify({...item,direction:download?'get':'send',device:session.device,hub:session.hub})});done++}
     M(`已复制 ${src.file} → ${dst.path}（${done} 项）`);
   }catch(e){M(`${(e as Error).message}${done?`；已复制 ${done} 项，已完成部分保留`:''}`)}finally{await client.invalidateQueries({queryKey:['disk',download?'/local/v1':'/connected/api/v1']});lock.current=false;B(false)}
 }
 return <><p role="status">{msg||'单击选中文件或文件夹，双击打开目录。在一栏按 Ctrl+C，在另一栏按 Ctrl+V。'}</p><section className="panes disk-panes"><Browser pos={local} set={L} other={remote} busy={busy} session={session} remote={false} shortcut={key=>shortcut(false,key)} copy={()=>copy(false)}/>{session?<Browser pos={remote} set={R} other={local} busy={busy} session={session} remote shortcut={key=>shortcut(true,key)} copy={()=>copy(true)}/>:<article className="pane disk-pane"><h2>远程服务器</h2><p>尚未连接。本机磁盘可独立浏览。</p><a href="/setup#join">连接服务器</a></article>}</section></>
}
export function Catalog(){const s=useQuery({queryKey:['disk-status'],queryFn:({signal})=>request<Session>('/local/v1/status',{signal}),refetchInterval:3000,retry:false});const session=s.data?.remoteAvailable&&s.data.agentStatus==='online'&&!s.error?s.data:undefined;return <main><div className="intro"><h1>磁盘文件传输</h1><a href="/setup">连接设置</a></div><Explorer key={session?.device??'offline'} session={session}/></main>}
