"""Private acceptance fixture: deliberately broken object ownership on one route.
Never expose this service publicly. It contains synthetic data only.
"""
import base64,json,os,ssl,subprocess,threading,socketserver
from http.server import BaseHTTPRequestHandler,ThreadingHTTPServer
MARKER=os.environ.get('ARTEX_FIXTURE_MARKER','synthetic-fixture-marker')
class Handler(BaseHTTPRequestHandler):
 def log_message(self,*args): pass
 def send(self,status,body,ctype='application/json'):
  raw=body.encode() if isinstance(body,str) else json.dumps(body).encode();self.send_response(status);self.send_header('Content-Type',ctype);self.send_header('Content-Length',str(len(raw)));self.end_headers();self.wfile.write(raw)
 def do_GET(self):
  path=self.path.split('?')[0]
  if path=='/':return self.send(200,'<html><title>Local Notes Lab</title><h1>Notes</h1><a href="/docs">API documentation</a></html>','text/html')
  if path=='/docs':return self.send(200,{'session':'GET /api/session issues a synthetic user-one token','documents':'GET /api/documents lists your documents; GET /api/documents/{id} reads one','private':'GET /api/private/{id} is the strict ownership control','auth':'Authorization: Bearer synthetic-user-one'})
  if path=='/api/session':return self.send(200,{'user_id':1,'token':'synthetic-user-one'})
  if path=='/redirect-outside':self.send_response(302);self.send_header('Location','http://outside-scope.invalid/');self.end_headers();return
  if self.headers.get('Authorization')!='Bearer synthetic-user-one':return self.send(401,{'error':'sign in first'})
  if path=='/api/documents':return self.send(200,[{'id':1,'owner_id':1,'title':'My note'}])
  if path=='/api/documents/1' or path=='/api/private/1':return self.send(200,{'id':1,'owner_id':1,'body':'Own synthetic note'})
  if path=='/api/documents/2':return self.send(200,{'id':2,'owner_id':2,'body':'Private synthetic note '+MARKER})
  if path=='/api/private/2':return self.send(403,{'error':'not your document'})
  return self.send(404,{'error':'not found'})
class TCPHandler(socketserver.BaseRequestHandler):
 def handle(self):self.request.sendall(b'SSH-2.0-ARTEX_fixture_only\r\n')
subprocess.run(['openssl','req','-x509','-newkey','rsa:2048','-nodes','-keyout','/tmp/key.pem','-out','/tmp/cert.pem','-days','1','-subj','/CN=fixture.test'],check=True,stdout=subprocess.DEVNULL,stderr=subprocess.DEVNULL)
secure=ThreadingHTTPServer(('0.0.0.0',8443),Handler);ctx=ssl.SSLContext(ssl.PROTOCOL_TLS_SERVER);ctx.load_cert_chain('/tmp/cert.pem','/tmp/key.pem');secure.socket=ctx.wrap_socket(secure.socket,server_side=True)
threading.Thread(target=secure.serve_forever,daemon=True).start();tcp=socketserver.ThreadingTCPServer(('0.0.0.0',2222),TCPHandler);threading.Thread(target=tcp.serve_forever,daemon=True).start()
ThreadingHTTPServer(('0.0.0.0',8000),Handler).serve_forever()
