import {test,expect,chromium} from '@playwright/test';
import {access,writeFile,readFile,readdir,stat,mkdir} from 'node:fs/promises';
import {createHash,randomUUID} from 'node:crypto';
import {join} from 'node:path';
import {fileURLToPath} from 'node:url';
import {startFixture,until} from '../../tests/e2e/fixture.mjs';

const exists=async (p:string)=>access(p).then(()=>true,()=>false);
const sha=(b:Buffer)=>createHash('sha256').update(b).digest('hex');
test('Windows NTFS copy, reconnect, lost receipt, no-overwrite and permissions',async()=>{
  test.skip(process.platform!=='win32','Windows write acceptance');
  test.setTimeout(110000);
  const f=await startFixture({write:true});
  try {
    const u=await f.login('reader'), other=await f.login('other');
    const post=(url:string,body:unknown,s=u,headers={})=>f.api(url,{method:'POST',body,session:s,headers});
    const get=(url:string,s=u)=>f.api(url,{session:s});
    async function makePlan(i:number, name:string,j:number,dest:string[]= [f.sub[j]], status=202){
      const listed=await f.expect(post('/fs/list',{ref:f.ref(i,[f.sub[i]])}),200);
      const version=listed.data.entries.find(x=>x.name===name)?.version;
      expect(version).toBeTruthy();
      return (await f.expect(post('/plans',{action:'copy',sources:[{ref:f.ref(i,[f.sub[i],name]),version}],destination:f.ref(j,dest),conflictPolicy:'skip'}),status)).data;
    }
    async function enqueue(planId:string,key=randomUUID()){
      return (await f.expect(post('/jobs',{planId},u,{'Idempotency-Key':key}),202)).data.jobId as string;
    }
    async function final(id:string,state:string,session=u){
      let last;
      await until(async()=>{last=(await get(`/jobs/${id}`,session)).data;return ['succeeded','skipped','cancelled','failed','needs_review'].includes(last?.state);},24000);
      expect(last,JSON.stringify(last)).toMatchObject({state});return last;
    }
    async function reconnect(i:number,phase?:string,marker?:string){
      await f.stopAgent(i);
      await until(async()=> (await get('/admin/nodes',f.admin)).data.some(x=>x.nodeId===f.nodes[i]&&!x.online));
      f.startAgent(i,phase,marker);
      await until(async()=> (await get('/admin/nodes',f.admin)).data.some(x=>x.nodeId===f.nodes[i]&&x.online));
    }
    const lp=join(f.paths[0],f.sub[0]),rp=join(f.paths[1],f.sub[1]);
    const name='跨分片 校验.bin',source=join(lp,name),target=join(rp,name);
    const data=Buffer.alloc((8<<20)+157);for(let i=0;i<data.length;i++)data[i]=(i*19)%251;
    await writeFile(source,data);
    const blockMarker=join(f.dir,'after-block');await reconnect(1,'after-block',blockMarker);
    const p=await makePlan(0,name,1);expect(p.target.nativePath).toBe(target);
    const key=randomUUID();const a=await enqueue(p.planId,key);expect(await enqueue(p.planId,key)).toBe(a);expect(await enqueue(p.planId)).toBe(a);
    await f.expect(get(`/jobs/${a}`,other),404);
    await until(()=>exists(blockMarker),18000);expect(await exists(target)).toBe(false);
    expect((await stat(join(f.paths[1],'.dockroom',a))).size).toBe(8<<20);
    await f.stopAgent(1);await f.restartHub();f.startAgent(1);
    const done=await final(a,'succeeded');expect(done.sha256).toBe(sha(data));expect(done.confirmedBytes).toBe(data.length);
    expect(sha(await readFile(target))).toBe(sha(data));expect(sha(await readFile(source))).toBe(sha(data));
    expect(Math.abs((await stat(source)).mtimeMs-(await stat(target)).mtimeMs)).toBeLessThan(1);
    const back=await makePlan(1,name,0,[f.sub[0],'空目录']);
    const commitMarker=join(f.dir,'after-publish');await reconnect(0,'after-publish',commitMarker);
    const b=await enqueue(back.planId);await until(()=>exists(commitMarker),18000);
    await f.stopAgent(0);await f.restartHub();f.startAgent(0);
    expect((await final(b,'succeeded')).sha256).toBe(sha(data));
    await until(async()=> (await get('/admin/nodes',f.admin)).data.filter(x=>x.online).length===2);
    expect(await readdir(join(lp,'空目录'))).toEqual([name]);expect(sha(await readFile(join(lp,'空目录',name)))).toBe(sha(data));
    const repeat=await makePlan(0,name,1);expect(repeat.target.state).toBe('exists');expect((await final(await enqueue(repeat.planId),'skipped')).errorCode).toBe('NAME_EXISTS');
    expect(sha(await readFile(target))).toBe(sha(data));
    await makePlan(0,name,0,[f.sub[0]],409);
    const moved=await makePlan(0,'索引 清单.csv',1);
    await writeFile(join(lp,'索引 清单.csv'),'changed since preflight');
    expect((await final(await enqueue(moved.planId),'failed')).errorCode).toBe('SOURCE_CHANGED');expect(await exists(join(rp,'索引 清单.csv'))).toBe(false);
    const zero=await makePlan(0,'封面 修订.tif',1);
    expect((await final(await enqueue(zero.planId),'succeeded')).sha256).toBe(sha(Buffer.alloc(0)));expect((await stat(join(rp,'封面 修订.tif'))).size).toBe(0);

    const cancelMarker=join(f.dir,'cancel-block');await reconnect(1,'after-block',cancelMarker);
    const cancelPlan=await makePlan(0,name,1,[f.sub[1],'空目录']);const cancelled=await enqueue(cancelPlan.planId);
    await until(()=>exists(cancelMarker));await f.expect(post(`/jobs/${cancelled}/cancel`,undefined),204);
    await final(cancelled,'cancelled');expect(await exists(join(rp,'空目录',name))).toBe(false);
    await reconnect(1);
    const race=await makePlan(0,'排版说明.pdf',1);
    const racePath=join(rp,'排版说明.pdf');await writeFile(racePath,'already here');
    await final(await enqueue(race.planId),'skipped');expect(await readFile(racePath,'utf8')).toBe('already here');
    const revokeMarker=join(f.dir,'revoke-block');await reconnect(1,'after-block',revokeMarker);
    const revokedPlan=await makePlan(0,name,1,[f.sub[1],'空目录']);const revoked=await enqueue(revokedPlan.planId);
    await until(()=>exists(revokeMarker));await f.grant('reader',1,true,false);
    const stopped=await final(revoked,'failed',f.admin);expect(stopped.errorCode).toBe('GRANT_REVOKED');
    expect(await exists(join(rp,'空目录',name))).toBe(false);await f.expect(get(`/jobs/${revoked}`),404);
    await f.grant('reader',0,true,false);await makePlan(1,name,0,[f.sub[0],'空目录'],403);
    console.log(`M2 PIDs ${f.pids().join('/')}; jobs ${a}, ${b}; ${data.length} bytes; SHA-256 ${sha(data)}; interrupted after 8388608 confirmed bytes and after publish before receipt; no duplicate`);
  }finally{await f.close();}
});

test('Windows browser clipboard, confirmation, background copy and final verification',async()=>{
  test.skip(process.platform!=='win32','Windows write acceptance');
  const f=await startFixture({write:true});
  const browser=await chromium.launch({channel:process.env.DOCKROOM_BROWSER_CHANNEL||undefined,args:[`--ignore-certificate-errors-spki-list=${f.spki}`]}).catch(async e=>{await f.close();throw e;});
  try{
    const name='浏览器 测试.bin', payload=Buffer.alloc((8<<20)+9,89);
    await writeFile(join(f.paths[0],f.sub[0],name),payload);
    const marker=join(f.dir,'browser-block');await f.stopAgent(1);f.startAgent(1,'after-block',marker);
    const u=await f.login('reader');await until(async()=> (await f.api('/nodes',{session:u})).data.filter(n=>n.online).length===2);
    const context=await browser.newContext({viewport:{width:1440,height:900}}),page=await context.newPage();page.setDefaultTimeout(9000);
    await page.goto(f.origin);await page.getByLabel('账号').fill('reader');await page.getByLabel('口令').fill(f.accounts.reader);await page.getByRole('button',{name:'登录',exact:true}).click();
    const left=page.getByRole('article',{name:'左栏目录'}),right=page.getByRole('article',{name:'右栏目录'});
    await left.getByRole('button',{name:'打开 2026'}).click();
    await right.getByRole('button',{name:'打开 十一月'}).click();
    const picked=right.getByRole('checkbox',{name:`选择 ${name}`});await picked.check();await picked.focus();await page.keyboard.press('Control+c');
    await expect(page.getByText(`studio-07 · ${name}`,{exact:true})).toBeVisible();
    await page.keyboard.press('Control+z');
    await expect(page.getByText(`studio-07 · ${name}`,{exact:true})).toHaveCount(0);
    await expect(picked).toBeChecked();
    await page.keyboard.press('Control+c');
    const filter=right.getByRole('textbox',{name:'按名称筛选'});
    await filter.focus();await page.keyboard.type('x');await page.keyboard.press('Control+z');
    await expect(filter).toHaveValue('');
    await expect(page.getByText(`studio-07 · ${name}`,{exact:true})).toBeVisible();
    await right.getByRole('button',{name:'上级'}).click();
    await left.focus();await page.keyboard.press('Control+v');
    await expect(page.getByRole('dialog')).toBeVisible();
    await page.keyboard.press('Control+z');
    await expect(page.getByRole('dialog')).toHaveCount(0);
    expect((await f.api('/jobs',{session:u})).data).toEqual([]);
    await left.focus();await page.keyboard.press('Control+v');
    const modal=page.getByRole('dialog');await expect(modal.getByText('来源 · studio-07')).toBeVisible();await expect(modal.getByText('目标 · archive-01')).toBeVisible();
    const evidence=fileURLToPath(new URL('../../.local/evidence/',import.meta.url));await mkdir(evidence,{recursive:true});
    await page.screenshot({path:join(evidence,'m2-confirm.png'),fullPage:true});
    await modal.getByRole('button',{name:'开始复制'}).click();
    await expect(modal).toHaveCount(0);
    await expect(page.getByRole('heading',{name:'文件工作台'})).toBeVisible();
    await expect(left.getByRole('textbox',{name:'原生绝对路径'})).toHaveValue(join(f.paths[1],f.sub[1]));
    await expect(right.getByRole('textbox',{name:'原生绝对路径'})).toHaveValue(f.paths[0]);
    await expect(page.getByRole('button',{name:'查看任务记录'})).toBeVisible();
    await page.close();
    await until(()=>exists(marker));await f.stopAgent(1);f.startAgent(1);
    const destination=join(f.paths[1],f.sub[1],name);
    await until(async()=> (await f.api('/jobs',{session:u})).data.some(j=>j.state==='succeeded'),22000);
    expect(sha(await readFile(destination))).toBe(sha(payload));
    const again=await context.newPage();again.setDefaultTimeout(9000);await again.goto(f.origin);await again.getByRole('button',{name:'任务记录'}).click();
    await expect(again.getByText('已完成',{exact:true})).toBeVisible();await expect(again.getByText(sha(payload),{exact:true})).toBeVisible();
    await again.screenshot({path:join(evidence,'m2-verified.png'),fullPage:true});
    await again.getByRole('button',{name:'定位目标目录'}).click();await expect(again.getByText(name,{exact:true})).toBeVisible();
    const newLeft=again.getByRole('article',{name:'左栏目录'}),newRight=again.getByRole('article',{name:'右栏目录'});
    await newRight.getByRole('button',{name:'打开 十一月'}).click();
    await newRight.getByRole('checkbox',{name:'选择 封面 修订.tif'}).check();
    await newRight.getByRole('button',{name:'复制文件'}).click();
    await newLeft.getByRole('button',{name:'粘贴到此处'}).click();
    await again.getByRole('dialog').getByRole('button',{name:'开始复制'}).click();
    await expect(again.getByText('最近复制：已完成',{exact:true})).toBeVisible();
    await expect(newLeft.getByText('封面 修订.tif',{exact:true})).toBeVisible();
    await expect(again.getByRole('heading',{name:'文件工作台'})).toBeVisible();
  }finally{await browser.close();await f.close();}
});
