'use strict';
// Runs inside the exact same namespace and network policy as assessment tools.
const fs = require('node:fs');
const net = require('node:net');
const http = require('node:http');
const { promisify } = require('node:util');
const execFile = promisify(require('node:child_process').execFile);
const checks = [];
async function check(name, fn) {
  try { const detail = await fn(); checks.push({name, status:'passed', required:true, detail:detail || 'Passed'}); }
  catch (e) { checks.push({name, status:'failed', required:true, detail:String(e.message).slice(0,1000)}); }
}
function connect(host, port) { return new Promise((resolve,reject)=>{
 const s=net.connect({host,port});s.setTimeout(700);s.once('connect',()=>{s.destroy();resolve();});s.once('error',reject);s.once('timeout',()=>{s.destroy();reject(Error('connection timed out'));});
}); }
function get(url, path) {return new Promise((resolve,reject)=>{
 const u=new URL(url);const req=http.request({host:u.hostname,port:u.port,path:path||u.pathname,headers:path?{Host:new URL(path).host}:{}},r=>{let body='';r.on('data',c=>body+=c);r.on('end',()=>resolve({status:r.statusCode,body}));});req.on('error',reject);req.setTimeout(3000,()=>req.destroy(Error('request timeout')));req.end();
});}
(async()=>{
 await check('filesystem_and_process_isolation',async()=>{
  const alias=process.env.ARTEX_HOST_WORKSPACE;
  if(alias && (fs.realpathSync(alias)!=='/workspace'||fs.statSync(alias).ino!==fs.statSync('/workspace').ino))throw Error('App-style task paths do not map to the persistent workspace');
  for(const p of ['/app/auth','/app/data','/run/artex-control-canary','/run/secrets','/var/run/docker.sock']) if(fs.existsSync(p))throw Error('Control path visible: '+p);
  for(const pid of fs.readdirSync('/proc').filter(x=>/^\d+$/.test(x))) {
   let env='';try{env=fs.readFileSync('/proc/'+pid+'/environ','utf8');}catch{}
   if(/ARTEX_PG_DSN=|ARTEX_RUNNER_TOKEN|OPENAI_API_KEY=|ARTEX_AUTH_DIR=/.test(env))throw Error('Control environment visible');
  }
  const status=fs.readFileSync('/proc/self/status','utf8');
  if(!/^CapEff:\s+0+$/m.test(status)||!/^CapBnd:\s+0+$/m.test(status)||!/^NoNewPrivs:\s+1$/m.test(status))throw Error('Capabilities or privilege escalation enabled');
  fs.writeFileSync('readiness-file.txt','42');if(fs.readFileSync('readiness-file.txt','utf8')!=='42')throw Error('Workspace write/read failed');
  return 'Task workspace writable; control paths/environment hidden; zero effective/bounding capabilities; no_new_privs';
 });
 await check('shell_and_dependencies',async()=>{
  const r=await execFile('sh',['-c','test "$(printf 42)" = 42 && command -v python3 && command -v curl && command -v rg && command -v nmap && command -v ssh && command -v dig && command -v ps && command -v pkill']);
  require('puppeteer-core');return 'Shell, Python, curl, ripgrep, TCP-connect Nmap, SSH, DNS diagnostics, process tools and Puppeteer present';
 });
 await check('direct_egress_denied',async()=>{
  const u=new URL('http://'+process.env.ARTEX_DENIED_PROBE_ADDRESS);
  let denied=false;try{await connect(u.hostname,Number(u.port));}catch{denied=true;}
  if(!denied)throw Error('Direct network route bypasses broker');
  let control=false;try{await connect('127.0.0.1',8080);}catch{control=true;}if(!control)throw Error('Runner control port reachable from tool sandbox');
  let dnsTCP=false;try{await connect('127.0.0.11',53);}catch{dnsTCP=true;}if(!dnsTCP)throw Error('Docker DNS TCP bypass reachable');
  let dnsUDP=false;try{await execFile('dig',['@127.0.0.11','broker','A','+time=1','+tries=1'],{timeout:2500});}catch(e){if(e.code==='ENOENT')throw e;dnsUDP=true;}if(!dnsUDP)throw Error('Docker DNS UDP bypass reachable');
  return 'Direct canary, runner control port and Docker DNS over TCP/UDP denied';
 });
 await check('broker_scope_and_budget',async()=>{
  const s=await get(process.env.ARTEX_BROKER_API+'/v1/status');const v=JSON.parse(s.body);
  if(s.status!==200||v.task_id!==process.env.ARTEX_TASK_ID||Date.parse(v.expires_at)<=Date.now())throw Error('Broker policy/task/time mismatch');
  const d=await get(process.env.HTTP_PROXY,'http://outside-scope.invalid/');if(d.status!==403)throw Error('Off-target proxy request not denied');
  return 'Task-bound durable budget available; off-target HTTP rejected';
 });
 const fixture=http.createServer((req,res)=>{res.setHeader('Content-Type','text/html');res.end('<button onclick="document.querySelector(\'output\').textContent=42">Calculate</button><output>0</output>');});
 await new Promise(r=>fixture.listen(0,'127.0.0.1',r));const url='http://127.0.0.1:'+fixture.address().port;
 await check('chromium_sandbox',async()=>{
  const {chromium}=require('playwright');const b=await chromium.launch({headless:true,executablePath:process.env.CHROMIUM,chromiumSandbox:true});
  try{const p=await b.newPage();await p.goto(url);await p.getByRole('button').click();if(await p.locator('output').textContent()!=='42')throw Error('Browser interaction failed');}finally{await b.close();}
  return 'Bundled Chromium launched with sandbox enabled and passed a real local click';
 });
 await check('browser_cli',async()=>{
  const opts={timeout:20000,env:{...process.env,PLAYWRIGHT_MCP_SANDBOX:'true'}};
  try{await execFile('playwright-cli',['-s=readiness','open',url],opts);const r=await execFile('playwright-cli',['-s=readiness','eval','() => document.querySelector("button").click() || document.querySelector("output").textContent'],opts);if(!r.stdout.includes('42'))throw Error(r.stdout+r.stderr);}finally{await execFile('playwright-cli',['-s=readiness','close'],opts).catch(()=>{});}
  return 'Locked browser CLI navigated and executed the local fixture';
 });
 await new Promise(r=>fixture.close(r));
 process.stdout.write(JSON.stringify(checks));
})().catch(e=>{process.stderr.write(String(e));process.exitCode=1;});
