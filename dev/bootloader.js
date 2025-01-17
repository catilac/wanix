if (!globalThis["ServiceWorkerGlobalScope"]) {
  const basePath = window.location.pathname.replace("index.html", "");

  // registers Service Worker using this file (see bottom) if none is registered,
  // and sets up a mechanism to fullfill requests from initfs or kernel
  async function setupServiceWorker() {
    const unzip = async (b64data) => {
      const gzipData = atob(b64data);
      const gzipBuf = new Uint8Array(gzipData.length);
      for (let i = 0; i < gzipData.length; i++) {
        gzipBuf[i] = gzipData.charCodeAt(i);
      }
      const gzipBlob = new Blob([gzipBuf], { type: 'application/gzip' });
      const ds = new DecompressionStream('gzip');
      const out = gzipBlob.stream().pipeThrough(ds);
      const response = new Response(out);
      const buf = await response.arrayBuffer();
      return new Uint8Array(buf);
    }

    let registration = await navigator.serviceWorker.getRegistration("/");
    if (!registration) {
      await navigator.serviceWorker.register("./wanix-bootloader.js?sw", {type: "module"});
      registration = await navigator.serviceWorker.ready;
      await new Promise((resolve) => {
        navigator.serviceWorker.addEventListener("controllerchange", async (event) => {
          resolve();
        });
      });
    }
    
    let readyResolver = undefined;
    const ready = new Promise((resolve) => {
      readyResolver = resolve;
    });

    navigator.serviceWorker.addEventListener("message", async (event) => {
      if (event.data.ready) {
        readyResolver();
        return;
      }
      const req = event.data.request;
      if (!req) {
        return;
      }

      // handle requests for compressed embedded initfs files if present
      if (globalThis.initdata && req.path.startsWith(`${basePath}~init/`)) {
        const f = globalThis.initdata[req.path.replace(`${basePath}~init/`, "")];
        if (f) {
          const data = await unzip(f.data);
          registration.active.postMessage({response: { reqId: req.id, body: data, headers: {"content-type": f.type}}});
          return;
        }
      }

      if (!globalThis.sys) {
        registration.active.postMessage({response: { reqId: req.id,  error: `kernel not loaded yet for ${req.path}` }});
        return;
      }

      // handle request using kernel via rpc
      const resp = await globalThis.sys.call("web.request", [req.method, req.url.replace(basePath, "/")]);
      const headers = resp.value;
      const ch = resp.channel;
      const buf = new duplex.Buffer();

      await duplex.copy(buf, ch);
      ch.close();

      registration.active.postMessage({response: { reqId: req.id, body: buf.bytes(), headers }});
    });

    registration.active.postMessage({init: true, basePath});
    await ready;
  }

  // bootloader starts here
  (async function() {
    console.log("Wanix booting...")
    await setupServiceWorker();

    globalThis.initfs = {};
    const load = async (path) => {
      const basename = (path) => path.replace(/\\/g,'/').split('/').pop();
      if (globalThis.initdata) {
        // use embedded data if present
        path = `./~init/${basename(path)}`;
      }
      globalThis.initfs[basename(path)] = await (await fetch(path)).blob();
    }
    // TODO: define these in one place. duplicated in initdata.go
    await Promise.all([
      load("./sys/dev/kernel/web/lib/duplex.js"),
      load("./sys/dev/kernel/web/lib/worker.js"),
      load("./sys/dev/kernel/web/lib/syscall.js"),
      load("./sys/dev/kernel/web/lib/task.js"),
      load("./sys/dev/kernel/web/lib/wasm.js"),
      load("./sys/dev/kernel/web/lib/host.js"),
      load("./sys/dev/internal/indexedfs/indexedfs.js"), // maybe load from kernel?
      load("./sys/dev/local/bin/kernel"),
      load("./sys/dev/local/bin/shell"),
      load("./sys/dev/local/bin/build"),
      load("./sys/dev/local/bin/micro"),
    ]);
    
    globalThis.duplex = await import(URL.createObjectURL(initfs["duplex.js"]));
    globalThis.task = await import(URL.createObjectURL(initfs["task.js"]));

    globalThis.sys = new task.Task(initfs);
    
    // start kernel
    console.log("Staring kernel...")
    await sys.exec("kernel");

    // load host API
    await import(URL.createObjectURL(initfs["host.js"]));

  })();
}

// this file is also used as the Service Worker source. 
// below is ignored unless in a Service Worker.
if (globalThis["ServiceWorkerGlobalScope"] && self instanceof ServiceWorkerGlobalScope) {
  let host = undefined;
  let responders = {};
  let reqId = 0;
  let basePath = "/";

  self.addEventListener("message", (event) => {
    if (event.data.init) {
      host = event.source;
      basePath = event.data.basePath;
      host.postMessage({ready: true});
      return;
    }
    if (responders && event.data.response) {
      responders[event.data.response.reqId](event.data.response);
    }
  });

  self.addEventListener("fetch", async (event) => {
    const req = event.request;
    const url = new URL(req.url);
    if (url.pathname === "/favicon.ico" || 
      url.pathname === basePath ||
      url.pathname.startsWith(`${basePath}wanix-bootloader.js`) ||
      url.pathname.startsWith(`${basePath}sys/dev`) || 
      url.pathname.startsWith(`${basePath}bootloader`) || 
      url.pathname.startsWith(`${basePath}index.html`) ||
      !host) return;

    reqId++;

    const headers = {}
    for (var p of req.headers) {
      headers[p[0]] = p[1]
    }

    const response = new Promise(resolve => {
      responders[reqId] = resolve;
    });
    event.respondWith(new Promise(async (resolve) => {
      host.postMessage({request: {method: req.method, url: req.url, path: url.pathname, headers: headers, id: reqId }});
      const reply = await response;
      if (reply.error) {
        console.warn(reply.error);
        resolve(Response.error());
        return;
      }
      resolve(new Response(reply.body, {headers: reply.headers}))
    }))
  });
  
  self.addEventListener('activate', event => {
    event.waitUntil(clients.claim());
  });

}
