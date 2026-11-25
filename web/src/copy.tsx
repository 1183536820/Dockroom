import {useEffect,useRef,useState} from 'react';
import {useQuery} from '@tanstack/react-query';
import {call} from './api';
import type {Contract} from './api';
import {workbenchShortcut} from './shortcuts';

type Plan=Contract['CopyPlan'];
export type Clipboard={source:Contract['CopySource']; name:string; native:string};
export function CopyConfirm({input,user,close,done}:{input:Contract['CopyInput'];user:Contract['Identity'];close:()=>void;done:(jobId:string)=>void}){
  const dialog=useRef<HTMLDialogElement>(null);
  const [plan,setPlan]=useState<Plan>();const [error,setError]=useState('');const [busy,setBusy]=useState(false);const key=useRef(crypto.randomUUID());
  useEffect(()=>{
    const modal=dialog.current;modal?.showModal();const ctl=new AbortController();
    call<Plan>('/plans',{method:'POST',body:input,csrf:user.csrf,signal:ctl.signal}).then(setPlan).catch(e=>{if(!ctl.signal.aborted)setError(e.message);});
    return ()=>{ctl.abort();modal?.close();};
  },[input,user.csrf]);
  async function confirm(){
    if(!plan||busy)return;setBusy(true);setError('');
    try{const job=await call<{jobId:string}>('/jobs',{method:'POST',csrf:user.csrf,idempotency:key.current,body:{planId:plan.planId}});done(job.jobId);}
    catch(e){setError((e as Error).message);}finally{setBusy(false);}
  }
  return <dialog ref={dialog} aria-labelledby="copy-heading" onCancel={event=>{event.preventDefault();if(!busy)close();}} onKeyDown={event=>{if(workbenchShortcut(event)==='z'){event.preventDefault();if(!busy)close();}}}>
    <h2 id="copy-heading">确认复制</h2>
    {!plan&&!error&&<p role="status">正在核对来源、目标与同名文件…</p>}
    {plan&&<><dl className="route"><dt>来源 · {input.sources[0].ref.nodeId}</dt><dd className="mono">{plan.source.nativePath}</dd><dt>目标 · {input.destination.nodeId}</dt><dd className="mono">{plan.target.nativePath}</dd></dl>
      <p>1 个文件 · {(plan.source.sizeBytes??0).toLocaleString()} 字节</p>
      <p className="muted">{plan.target.state==='exists'?'检测到同名，提交后将跳过。':'如果目标出现同名文件，任务会跳过。'}源文件保持不变。</p></>}
    {error&&<p className="error" role="alert">{error}</p>}
    <div className="dialog-actions"><button autoFocus disabled={busy} onClick={close}>返回</button><button className="primary" disabled={busy||!plan} onClick={confirm}>{busy?'正在提交…':'开始复制'}</button></div>
  </dialog>;
}

const labels:Record<Contract['JobState'],string>={queued:'排队中',running:'复制中',waiting_node:'等待端点重连',verifying:'校验中',committing:'正在确认发布',succeeded:'已完成',skipped:'已跳过',failed:'失败',cancelled:'已取消',needs_review:'需人工核对'};
const active=new Set(['queued','running','waiting_node','verifying','committing']);
export {labels as jobLabels, active as activeJobStates};
const errors:Record<string,string>={NAME_EXISTS:'目标已存在，原有内容未被覆盖',SOURCE_CHANGED:'源文件已改动，请刷新后重新选择',GRANT_REVOKED:'源或目标授权已撤销',COMMIT_UNKNOWN:'不能确认发布是否已完成，请核对目标文件',NODE_OFFLINE:'设备连接中断',STAGING_MISMATCH:'暂存摘要或身份不匹配',TRANSFER_INTERRUPTED:'读写或网络中断'};
export function Tasks({user,locate}:{user:Contract['Identity'];locate:(r:Contract['Ref'])=>void}){
  const [error,setError]=useState('');
  const list=useQuery({queryKey:['copy-jobs',user.user],queryFn:({signal})=>call<Contract['CopyJob'][]>('/jobs',{signal}),refetchInterval:1000});
  async function cancel(id:string){setError('');try{await call(`/jobs/${id}/cancel`,{method:'POST',csrf:user.csrf});await list.refetch();}catch(e){setError((e as Error).message);}}
  return <section className="task-list" aria-label="复制任务">
    {list.isPending&&<p role="status">正在读取任务…</p>}
    {(error||list.error)&&<p className="error" role="alert">{error||list.error?.message}</p>}
    {list.data?.length===0&&<p>还没有复制任务。</p>}
    {list.data?.map(j=><article className="task" key={j.jobId}>
      <div className="task-head"><strong>{labels[j.state]}{j.cancelRequested&&active.has(j.state)?' · 已请求取消':''}</strong><span className="muted">{new Date(j.createdUnix*1000).toLocaleString()} · {j.owner}</span></div>
      <dl className="route"><dt>{j.plan.input.sources[0].ref.nodeId} → {j.plan.input.destination.nodeId}</dt><dd className="mono">{j.plan.source.nativePath}</dd><dd className="mono">{j.plan.target.nativePath}</dd></dl>
      <p className="mono">已确认 {j.confirmedBytes.toLocaleString()} / {(j.plan.source.sizeBytes??0).toLocaleString()} B</p>
      {j.state==='succeeded'&&j.sha256&&<p className="digest"><b>SHA-256 已校验：</b><code>{j.sha256}</code></p>}
      {j.errorCode&&<p className="muted">{errors[j.errorCode]??j.errorCode}</p>}
      <div className="task-head"><small className="mono">任务 {j.jobId}</small>{active.has(j.state)&&<button disabled={j.cancelRequested} onClick={()=>cancel(j.jobId)}>请求取消</button>}{j.state==='succeeded'&&<button onClick={()=>locate(j.plan.input.destination)}>定位目标目录</button>}</div>
    </article>)}
  </section>;
}
