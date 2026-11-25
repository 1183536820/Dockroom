import {useState} from 'react';
import type {FormEvent} from 'react';
import {useQuery,useQueryClient} from '@tanstack/react-query';
import {call} from './api';
import type {Contract} from './api';

type Directory = {users: Pick<Contract['Identity'],'user'|'admin'>[]; roots:(Contract['Root']&{nodeId:string})[]};
export function Devices({user}: {user: Contract['Identity']}) {
  const query=useQueryClient();const nodes=useQuery({queryKey:['admin-nodes'],queryFn:()=>call<Contract['Node'][]>('/admin/nodes'),refetchInterval:2500});
  const directory=useQuery({queryKey:['admin-users'],queryFn:()=>call<Directory>('/admin/users'),refetchInterval:2500});
  const pairing=useQuery({queryKey:['pairing'],queryFn:()=>call<{active:boolean;code:string}>('/admin/pairing')});
  const [key,setKey]=useState('');const [showKey,setShowKey]=useState(false);const [msg,setMsg]=useState('');const [error,setError]=useState('');const [busy,setBusy]=useState(false);
  const [legacy,setLegacy]=useState<string>();
  const isDesktop=location.protocol==='http:'&&/^dockroom-[a-f0-9]+\.localhost$/.test(location.hostname);
  async function submit(e:FormEvent<HTMLFormElement>, task:(d:FormData)=>Promise<void>) {
    e.preventDefault();setBusy(true);setMsg('');setError('');try{await task(new FormData(e.currentTarget));query.invalidateQueries();}catch(e){setError((e as Error).message);}finally{setBusy(false);}
  }
  function post<T>(path:string,body?:unknown,method='POST'){return call<T>(path,{method,body,csrf:user.csrf});}
  async function change(action:'enable'|'rotate'|'disable'){
    const result=await post<{key:string}|undefined>('/admin/pairing',{action});setKey(result?.key??'');setShowKey(false);
    setMsg(action==='disable'?'已停用新设备接入，已连接设备不受影响。':'连接密钥仅本次显示，请妥善保存。');
  }
  async function copySecret(text:string,label:string){try{await navigator.clipboard.writeText(text);setMsg(`${label}已复制`);}catch{setError('无法复制，请手动选中后按 Ctrl+C');}}
  return <div className="devices">
    <section><h2>让其他电脑加入</h2><p>把连接码和密钥发给可信的人。对方在同版本 EXE 里输入两项即可接入，不需要逐台批准。</p>
      {pairing.error&&<p role="alert">{pairing.error.message}</p>}
      {pairing.data?.active?<div className="invite-result"><label>主机连接码<textarea className="mono" rows={3} value={pairing.data.code} readOnly onFocus={event=>event.currentTarget.select()}/></label><button onClick={()=>copySecret(pairing.data.code,'连接码')}>复制连接码</button>
        {key&&<div><label>本次生成的连接密钥 <input type={showKey?'text':'password'} value={key} readOnly autoComplete="off"/></label><button onClick={()=>copySecret(key,'密钥')}>复制密钥</button>{' '}<button onClick={()=>setShowKey(v=>!v)}>{showKey?'隐藏密钥':'显示密钥'}</button></div>}
        {!key&&<p>使用原连接密钥。遗失时可更换密钥。</p>}
        <form onSubmit={e=>{e.preventDefault();const action=(e.nativeEvent as SubmitEvent).submitter?.getAttribute('value');if(action==='rotate'||action==='disable'){if(window.confirm(action==='rotate'?'更换密钥？旧密钥将不能接入新设备。':'停用后，持有密钥的人不能再接入新设备。确定停用？'))void submit(e,()=>change(action));}}}><button value="rotate" disabled={busy}>更换密钥</button><button value="disable" disabled={busy}>停用新设备接入</button></form>
      </div>:<form onSubmit={e=>submit(e,()=>change('enable'))}><button className="primary" disabled={busy||!pairing.data}>生成连接码和密钥</button></form>}
      {isDesktop&&<p><a href="/setup#join">输入其他电脑的连接码和密钥</a></p>}
      <details><summary>兼容旧版命令行 Agent</summary><form onSubmit={e=>submit(e,async d=>{setLegacy(undefined);const r=await post<{code:string}>('/enrollments',{nodeId:d.get('node')});setLegacy(r.code);})}><label>设备编号 <input name="node" placeholder="studio-07" required /></label><button disabled={busy}>生成旧版接入码</button></form>{legacy&&<p><code className="mono">{legacy}</code> <button onClick={()=>setLegacy(undefined)}>隐藏</button></p>}</details>
    </section>
    <section><h2>已登记设备</h2>{nodes.error&&<p role="alert">{nodes.error.message}</p>}
      {nodes.data?.length===0&&<p>尚无已登记设备。</p>}
      {nodes.data?.map(n=><div className="device" key={n.nodeId}><b className="mono">{n.nodeId}</b> · {n.state==='pending'?'待批准':n.state==='revoked'?'已撤销':n.online?'在线':'离线'} {n.os&&` · ${n.os} / ${n.version}`}
        {n.state==='pending'&&<><p>核对码 <b className="mono">{n.fingerprint?.slice(0,12).match(/.{4}/g)?.join('-').toUpperCase()}</b>（应与另一台电脑显示一致）</p><form onSubmit={e=>submit(e,async()=>{await post(`/nodes/${n.nodeId}/approve`,{fingerprint:n.fingerprint});setMsg('设备已批准，等待上线');})}>
          <label><input name="matched" type="checkbox" required/> 两端核对码一致</label> <button disabled={busy}>批准连接</button>
        </form></>}
        {n.online&&directory.data?.roots.filter(r=>r.nodeId===n.nodeId&&r.rootId==='files').map(r=><form key={r.rootId} onSubmit={e=>submit(e,async()=>{await post(`/users/${user.user}/grants`,{nodeId:r.nodeId,rootId:r.rootId,read:true,write:!!r.canWrite},'PUT');setMsg('已授权本账号。工作台下拉可以选择这台电脑。');})}><button disabled={busy}>允许我访问「{r.label}」{r.canWrite?' · 可收发':' · 只读'}</button></form>)}
        {n.state!=='revoked'&&<form onSubmit={e=>{e.preventDefault();if(window.confirm(`撤销 ${n.nodeId}？将断开连接并清除该设备的授权。`))void submit(e,async()=>{await post(`/nodes/${n.nodeId}/revoke`);setMsg('设备已撤销');});}}><button className="danger" disabled={busy}>撤销设备</button></form>}
      </div>)}
    </section>
    <section><h2>根读取授权</h2>{directory.error&&<p role="alert">{directory.error.message}</p>}
      <form onSubmit={e=>submit(e,async d=>{const [nodeId,rootId]=String(d.get('root')).split('/');await post(`/users/${d.get('user')}/grants`,{nodeId,rootId,read:d.get('action')!=='revoke',write:d.get('action')==='write'},'PUT');setMsg('根授权已更新');})}>
        <label>账号 <select name="user">{directory.data?.users.map(u=><option key={u.user}>{u.user}</option>)}</select></label>{' '}
        <label>授权根 <select name="root">{directory.data?.roots.map(r=><option key={`${r.nodeId}/${r.rootId}`} value={`${r.nodeId}/${r.rootId}`}>{r.nodeId} · {r.label}</option>)}</select></label>{' '}
        <label>操作 <select name="action"><option value="grant">仅授予读取</option><option value="write">授予读取及复制写入</option><option value="revoke">撤销授权</option></select></label>{' '}
        <button className="primary" disabled={busy||!directory.data?.roots.length}>应用</button>
      </form>
    </section>
    {msg&&<p role="status">{msg}</p>}{error&&<p className="error" role="alert">{error}</p>}
  </div>;
}
