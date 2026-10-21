"""Observe installed CLI browser launch shape without consent or credential exchange."""
import json, os, pathlib, shutil, signal, subprocess, sys, tempfile, time
with tempfile.TemporaryDirectory(prefix='in-cli-') as tmp:
    root=pathlib.Path(tmp)
    browser=root/'browser'
    browser.write_text('#!'+sys.executable+'\n'+'''import json, os, sys
from pathlib import Path
from urllib.parse import urlsplit, parse_qs
u=urlsplit(sys.argv[-1]);q=parse_qs(u.query)
r=urlsplit(q.get('redirect_uri',[''])[0])
Path(os.environ['IN_PROBE_OUT']).write_text(json.dumps({'launch_scheme':u.scheme,'launch_host':u.hostname,'launch_port':u.port,'parameter_names':sorted(q),'callback_host':r.hostname,'callback_port':r.port}))
''')
    browser.chmod(0o700)
    for name,argv in [('wrangler',['wrangler','login']),('claude',['claude','auth','login']),('rclone',['rclone','authorize','drive','--config',str(root/'rclone.conf')])]:
        if not shutil.which(name):
            print(name, 'not installed', flush=True)
            continue
        env={k:v for k,v in os.environ.items() if not k.startswith(('AWS_', 'GOOGLE_', 'AZURE_', 'CLAUDE_')) and not any(word in k.upper() for word in ('TOKEN','API_KEY','SECRET','CLOUDFLARE','ANTHROPIC'))}
        env.update(BROWSER=str(browser),IN_PROBE_OUT=str(root/(name+'.json')),XDG_CONFIG_HOME=str(root/'config'),XDG_STATE_HOME=str(root/'state'),WRANGLER_HOME=str(root/'wrangler'),WRANGLER_LOG_PATH=str(root/'wrangler.log'),CLAUDE_CONFIG_DIR=str(root/'claude'),WRANGLER_SEND_METRICS='false',DISABLE_TELEMETRY='1',DISABLE_AUTOUPDATER='1',CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC='1')
        env.pop('DISPLAY',None);env.pop('WAYLAND_DISPLAY',None)
        version=subprocess.run([name,'--version'] if name!='rclone' else [name,'version'],env=env,capture_output=True,text=True,timeout=15).stdout.splitlines()
        proc=subprocess.Popen(argv,env=env,stdin=subprocess.DEVNULL,stdout=subprocess.DEVNULL,stderr=subprocess.DEVNULL,start_new_session=True)
        try:
            until=time.monotonic()+15
            while not (root/(name+'.json')).exists() and proc.poll() is None and time.monotonic()<until:time.sleep(.05)
            metadata=json.loads((root/(name+'.json')).read_text()) if (root/(name+'.json')).exists() else {'hook_observed':False,'exit':proc.poll()}
            print(name,version[:1],metadata,flush=True)
        finally:
            try:os.killpg(proc.pid,signal.SIGTERM)
            except ProcessLookupError:pass
            try:proc.wait(timeout=3)
            except subprocess.TimeoutExpired:os.killpg(proc.pid,signal.SIGKILL);proc.wait()
