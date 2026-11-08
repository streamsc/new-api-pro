import concurrent.futures, contextlib, http.client, json, os, pathlib, secrets, signal, sqlite3, statistics, subprocess, sys, threading, time, urllib.request, urllib.error
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
ROOT=pathlib.Path(__file__).resolve().parent
RUN=ROOT/('live-'+time.strftime('%Y%m%d-%H%M%S'))
RUN.mkdir()
PROCS=[]; LOGS=[]; AUTH={}; KEY={}; CHANNEL={}; RESULTS=[]
BASE='/api/affinity-diagnostics'
PASSWORD='Dg!'+secrets.token_urlsafe(24)
SECRET=secrets.token_hex(32)
lock=threading.Lock(); received=[]; retries=set()
class Upstream(BaseHTTPRequestHandler):
 def log_message(self,*args): pass
 def do_POST(self):
  raw=self.rfile.read(int(self.headers.get('Content-Length',0)))
  data=json.loads(raw); marker=data.get('metadata',{}).get('session','')
  with lock:
   received.append((marker,raw))
   fail=marker.startswith('retry-') and marker not in retries
   retries.add(marker)
  if fail:
   self.send_response(500); self.send_header('Content-Type','application/json'); self.end_headers(); self.wfile.write(b'{"error":{"message":"acceptance transient","type":"server_error"}}'); return
  result={'id':'chatcmpl-acceptance','object':'chat.completion','created':int(time.time()),'model':data['model'],'choices':[{'index':0,'message':{'role':'assistant','content':'acceptance ok'},'finish_reason':'stop'}],'usage':{'prompt_tokens':10,'completion_tokens':2,'total_tokens':12}}
  self.send_response(200); self.send_header('Content-Type','application/json'); self.end_headers(); self.wfile.write(json.dumps(result).encode())
server=ThreadingHTTPServer(('127.0.0.1',33690),Upstream)
threading.Thread(target=server.serve_forever,daemon=True).start()
client=urllib.request.build_opener(urllib.request.ProxyHandler({}))
def call(port,path,body=None,method=None,auth=True,headers=None):
 h={'Content-Type':'application/json'}
 if auth and port in AUTH: h.update(AUTH[port])
 h.update(headers or {})
 raw=body if isinstance(body,bytes) else (None if body is None else json.dumps(body).encode())
 req=urllib.request.Request(f'http://127.0.0.1:{port}'+path,data=raw,headers=h,method=method)
 try: response=client.open(req,timeout=15)
 except urllib.error.HTTPError as error: response=error
 data=response.read()
 try: value=json.loads(data)
 except Exception: value=data
 return response.status,dict(response.headers),value,data

def ok(port,path,body=None,method=None):
 status,_,value,_=call(port,path,body,method)
 assert status==200 and value.get('success'),(path,status,value)
 return value.get('data')

def wait_for(check,timeout=12):
 end=time.monotonic()+timeout
 while time.monotonic()<end:
  value=check()
  if value: return value
  time.sleep(.1)
 raise AssertionError('Timed out waiting for acceptance condition')

def passed(name,**data):
 item={'PASS':name,**data}; RESULTS.append(item); print(json.dumps(item,ensure_ascii=False),flush=True)
 (RUN/'result.json').write_text(json.dumps(RESULTS,ensure_ascii=False,indent=2))

def launch(port,mode,db):
 env={**os.environ,'PORT':str(port),'METRICS_PORT':str(port+100),'GIN_MODE':'release','NODE_NAME':f'issue17-{port}','GATEWAY_REPLICA_MODE':mode,'SESSION_SECRET':SECRET,'CRYPTO_SECRET':SECRET,'SESSION_COOKIE_SECURE':'false','SQL_DSN':'','LOG_SQL_DSN':'','REDIS_CONN_STRING':'','PASSWORD_LOGIN_ENCRYPTION_ENABLED':'false','MEMORY_CACHE_ENABLED':'false','SYNC_FREQUENCY':'1','POLLING_INTERVAL':'10','GLOBAL_API_RATE_LIMIT':'10000','GLOBAL_WEB_RATE_LIMIT':'10000'}
 if mode=='single': env['SQLITE_PATH']=str(db)
 else: env.update(SQL_DSN=f'root:diagnostic-test@tcp(127.0.0.1:33571)/{db}?parseTime=true&charset=utf8mb4',REDIS_CONN_STRING='redis://127.0.0.1:33573/5')
 log=open(RUN/f'gateway-{port}.log','ab'); LOGS.append(log)
 p=subprocess.Popen([str(ROOT/'gateway'),'-log-dir',str(RUN/f'logs-{port}')],cwd=RUN,env=env,stdout=log,stderr=subprocess.STDOUT)
 PROCS.append(p)
 def ready():
  if p.poll() is not None: raise AssertionError(f'gateway {port} failed; see log')
  try: return call(port,'/api/setup',auth=False)[0]==200
  except Exception:return False
 wait_for(ready,25)
 return p

def login(port):
 value=ok(port,'/api/user/login',{'username':'diagadmin','password':PASSWORD})
 AUTH[port]={'Authorization':'Bearer '+value['access_token'],'New-Api-User':str(value['user']['id'])}
 return value

def setup(port):
 ok(port,'/api/setup',{'username':'diagadmin','password':PASSWORD,'confirmPassword':PASSWORD,'SelfUseModeEnabled':False,'DemoSiteEnabled':False})
 bundle=login(port)
 ok(port,'/api/option/',{'key':'RetryTimes','value':'1'},'PUT')
 ok(port,'/api/option/',{'key':'performance_setting.monitor_disk_threshold','value':'99'},'PUT')
 ok(port,'/api/channel/',{'mode':'single','channel':{'type':1,'name':'issue17-local-upstream','key':'local-upstream-test-key','status':1,'base_url':'http://127.0.0.1:33690','models':'gpt-4o-mini','group':'default','priority':1}})
 channels=ok(port,'/api/channel/?p=0&page_size=10')
 CHANNEL[port]=channels['items'][0]['id']
 ok(port,'/api/token/',{'name':'issue17-local-client','expired_time':-1,'unlimited_quota':True,'group':'default'})
 tokens=ok(port,'/api/token/?p=0&page_size=10')
 KEY[port]=ok(port,f"/api/token/{tokens['items'][0]['id']}/key",{})['key']
 return bundle

def body(marker,padding=0):
 return (' { "model":"gpt-4o-mini", "messages":[{"role":"user","content":"hello"}], "metadata":{"session":"'+marker+'","dup":"first","dup":"last"}, "padding":"'+('x'*padding)+'" }\n').encode()

def relay(port,raw,fixed=False):
 key=KEY[port]+('-'+str(CHANNEL[port]) if fixed else '')
 start=time.perf_counter_ns()
 result=call(port,'/v1/chat/completions',raw,auth=False,headers={'Authorization':'Bearer '+key,'X-Session':'network-session','X-Private':'excluded-value','User-Agent':'issue17-live-client'})
 assert result[0]==200,(result[0],result[2])
 assert result[2]['choices'][0]['message']['content']=='acceptance ok',result[2]
 return (time.perf_counter_ns()-start)/1e6

def create(port,**kwargs):
 return ok(port,BASE,{'user_id':1,'capture_body':True,'excluded_headers':['X-Private'],**kwargs})

def samples(port,task): return ok(port,f"{BASE}/{task['id']}/samples")
def taskrow(port,task): return next((r for r in ok(port,BASE)['items'] if r['id']==task['id']),None)
def saved(port,task,n): return wait_for(lambda: (lambda v:v if v['total']>=n else None)(samples(port,task)))
def stop(port,task): return ok(port,f"{BASE}/{task['id']}/stop",{})
def remove(port,task):
 ok(port,f"{BASE}/{task['id']}",method='DELETE')
 assert call(port,f"{BASE}/{task['id']}/samples")[0]==410

def timing(values):
 values=sorted(values)
 return {'count':len(values),'mean_ms':round(statistics.mean(values),3),'p95_ms':round(values[min(len(values)-1,int(len(values)*.95))],3),'p99_ms':round(values[min(len(values)-1,int(len(values)*.99))],3)}

def main():
 single=launch(33691,'single',RUN/'single.db')
 bundle=setup(33691)
 off=[relay(33691,body('off')) for _ in range(20)]
 task=create(33691)
 captured=[relay(33691,body(f'capture-{i}'),fixed=(i==0)) for i in range(20)]
 listing=saved(33691,task,20)
 first=next(r for r in listing['items'] if ok(33691,f"{BASE}/{task['id']}/samples/{r['id']}")['metadata']['affinity_bypass']=='fixed_channel')
 detail=ok(33691,f"{BASE}/{task['id']}/samples/{first['id']}")
 assert detail['body'].encode()==body('capture-0')
 assert 'Authorization' not in detail['metadata']['headers'] and 'X-Private' not in detail['metadata']['headers']
 for source,expected in [({'type':'gjson','path':'metadata.session'},'capture-0'),({'type':'request_header','key':'X-Session'},'network-session'),({'type':'context_int','key':'id'},'1'),({'type':'context_string','key':'user_group'},'default')]:
  value=ok(33691,f"{BASE}/{task['id']}/samples/{first['id']}/extract",source)
  assert value['value']==expected and value['status']=='extracted',value
 download=call(33691,f"{BASE}/{task['id']}/samples/{first['id']}?download=body")
 assert download[3]==body('capture-0') and download[1].get('Cache-Control')=='no-store'
 assert call(33691,BASE,auth=False)[0]==401
 stop(33691,task); relay(33691,body('after-stop')); assert samples(33691,task)['total']==20
 passed('single-node real HTTP, fixed channel, four-source extraction, exact download, headers, stop',samples=20,disabled=timing(off),enabled=timing(captured))
 remove(33691,task)
 passed('single-node delete immediately fences list and downloads')
 retry=create(33691)
 relay(33691,body('retry-once'))
 saved(33691,retry,1)
 assert sum(marker=='retry-once' for marker,_ in received)==2
 assert samples(33691,retry)['total']==1
 passed('real upstream retry captures one external request',upstream_attempts=2,samples=1)
 remove(33691,retry)
 restart=create(33691); relay(33691,body('restart')); saved(33691,restart,1)
 single.terminate(); single.wait(timeout=15)
 launch(33691,'single',RUN/'single.db'); login(33691)
 row=taskrow(33691,restart)
 assert row['status']=='stopped' and row['reason']=='process_restart',row
 assert samples(33691,restart)['total']==1
 passed('single-node process restart ends task and preserves saved sample')
 remove(33691,restart)
 # Leave one stopped sample for real browser inspection.
 visual=create(33691); relay(33691,body('browser-session')); saved(33691,visual,1); stop(33691,visual)
 state={'username':'diagadmin','password':PASSWORD,'port':33691,'task_id':visual['id'],'run':str(RUN)}
 path=ROOT/'live-browser-private.json'; path.write_text(json.dumps(state)); path.chmod(0o600)
 db='issue17live_'+secrets.token_hex(4)
 # DB 5 belongs exclusively to this harness in the issue17 test container.
 subprocess.run(['docker','exec','issue17-redis','redis-cli','-n','5','FLUSHDB'],check=True,stdout=subprocess.DEVNULL)
 subprocess.run(['docker','exec','-e','MYSQL_PWD=diagnostic-test','issue17-mysql','mysql','-uroot','-e',f'CREATE DATABASE {db} CHARACTER SET utf8mb4'],check=True,stdout=subprocess.DEVNULL)
 launch(33692,'cluster',db); setup(33692)
 launch(33693,'cluster',db); login(33693); KEY[33693]=KEY[33692]; CHANNEL[33693]=CHANNEL[33692]
 task=create(33692,max_count=10)
 assert call(33693,BASE,{'user_id':1})[0]==409
 # Polling snapshot is intentionally allowed up to two seconds.
 time.sleep(2.1)
 with concurrent.futures.ThreadPoolExecutor(max_workers=4) as pool:
  latencies=list(pool.map(lambda i:relay(33692+i%2,body(f'dual-{i}')),range(16)))
 result=saved(33692,task,10)
 wait_for(lambda:taskrow(33692,task)['status']=='stopped')
 row=taskrow(33692,task)
 assert result['total']==10 and row['requested_count']==10 and row['reason']=='count_limit',row
 nodes={ok(33692,f"{BASE}/{task['id']}/samples/{r['id']}")['metadata']['node'] for r in result['items']}
 assert len(nodes)==2,nodes
 passed('dual real gateways with shared MySQL and Redis enforce one task and global quota',requests=16,samples=10,nodes=sorted(nodes),latency=timing(latencies))
 remove(33693,task)
 assert call(33692,f"{BASE}/{task['id']}/samples")[0]==410
 passed('delete on second node fences reads on first node')
 short=create(33692,duration_seconds=2)
 subprocess.run(['docker','stop','issue17-redis'],check=True,stdout=subprocess.DEVNULL)
 relay(33692,body('redis-outage'))
 # Existing global management API rate limiting requires Redis, so inspect the
 # task's durable state directly while Redis is unavailable, then verify API.
 def durable_deadline():
  text=subprocess.check_output(['docker','exec','-e','MYSQL_PWD=diagnostic-test','issue17-mysql','mysql','-uroot','-N','-B',db,'-e',f"SELECT status,reason FROM affinity_diagnostic_tasks WHERE id={short['id']}"],text=True).strip()
  return text=='stopped\tdeadline'
 wait_for(durable_deadline,timeout=12)
 passed('Redis outage keeps business request successful and task deadline still stops capture')
 subprocess.run(['docker','start','issue17-redis'],check=True,stdout=subprocess.DEVNULL)
 wait_for(lambda:call(33692,BASE)[0]==200)
 row=taskrow(33692,short); assert row['status']=='stopped' and row['reason']=='deadline',row
 passed('acceptance complete; real browser gateway available',port=33691,run=str(RUN))
 if '--serve' in sys.argv: threading.Event().wait()

if __name__=='__main__':
 def shutdown(*args): raise KeyboardInterrupt()
 signal.signal(signal.SIGTERM,shutdown)
 try: main()
 finally:
  for p in reversed(PROCS):
   if p.poll() is None:
    p.terminate()
    try:p.wait(timeout=15)
    except subprocess.TimeoutExpired:p.kill();p.wait()
  server.shutdown()
  for log in LOGS: log.close()
