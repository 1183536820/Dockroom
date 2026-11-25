import {chromium} from '@playwright/test';
import {writeFile,mkdir} from 'node:fs/promises';
import {join} from 'node:path';
import {startFixture} from '../../tests/e2e/fixture.mjs';

// Local interactive preview, using the same real service binaries and test
// root provisioning as the Windows end-to-end suite. No fault injection.
const fixture=await startFixture({write:true});
let context;
try{
  const source=join(fixture.paths[0],fixture.sub[0]);
  await writeFile(join(source,'渡柜演示.txt'),'渡柜 · 合成演示文件\r\n复制到右栏后，在任务记录里查看目标路径和 SHA-256。\r\n');
  await writeFile(join(source,'跨分片 样本.bin'),Buffer.alloc((8<<20)+17,0x44));
  const session=await fixture.login('reader');
  const split=session.cookie.indexOf('=');
  context=await chromium.launchPersistentContext(join(fixture.dir,'demo-browser'),{
    channel:process.env.DOCKROOM_BROWSER_CHANNEL||'chrome',headless:false,viewport:null,
    args:[`--ignore-certificate-errors-spki-list=${fixture.spki}`,'--start-maximized'],
  });
  await context.addCookies([{name:session.cookie.slice(0,split),value:session.cookie.slice(split+1),url:fixture.origin,httpOnly:true,secure:true,sameSite:'Strict'}]);
  const page=context.pages()[0]??await context.newPage();
  // Initialize only this disposable browser profile's workbench positions.
  await context.addInitScript(({origin,positions})=>{
    if(location.origin!==origin)return;
    for(const [i,ref] of positions.entries()){
      const key=`dockroom.m1.reader.${i}`;
      if(!localStorage.getItem(key))localStorage.setItem(key,JSON.stringify({ref}));
    }
  },{origin:fixture.origin,positions:[fixture.ref(0,[fixture.sub[0]]),fixture.ref(1,[fixture.sub[1]])]});
  await page.goto(fixture.origin);
  await page.getByRole('checkbox',{name:'选择 渡柜演示.txt',exact:true}).waitFor();
  await page.getByRole('article',{name:'右栏目录'}).getByText('交接说明.txt',{exact:true}).waitFor();
  await page.bringToFront();
  const evidence=join(fixture.dir,'..','evidence');await mkdir(evidence,{recursive:true});
  const screenshot=join(evidence,'windows-live-demo.png');
  await page.screenshot({path:screenshot});
  console.log(JSON.stringify({status:'ready',origin:fixture.origin,pids:fixture.pids(),screenshot}));
  if(!page.isClosed())await new Promise(resolve=>page.once('close',resolve));
}finally{
  await context?.close().catch(()=>{});
  await fixture.close();
}
