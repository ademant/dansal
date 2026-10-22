// dansal service worker (#1589). Served at /sw.js, so its scope is the whole
// site. __VERSION__ is replaced with the build version by the server.
//
// What it caches: the offline page, the manifest and icons, and /static/ assets
// (base.js carries ?v=<version>, so a new build fetches a new copy). It never
// caches HTML pages, because a page can carry personal data. A navigation made
// without a connection gets the offline page instead.

var VERSION = "__VERSION__";
var CACHE = "dansal-" + VERSION;
var PRECACHE = [
  "/offline",
  "/manifest.json",
  "/favicon.svg",
  "/logo.avif",
  "/icon-192.png",
  "/icon-512.png",
  "/static/base.js?v=" + VERSION
];

// Paths the worker never handles: admin and account pages, the API and the
// internal relay. Their responses always come from the server.
var BYPASS = ["/admin", "/api/", "/internal/", "/settings", "/login", "/logout", "/register", "/oidc", "/dashboard"];

self.addEventListener("install", function (event) {
  event.waitUntil(
    caches.open(CACHE)
      .then(function (cache) { return cache.addAll(PRECACHE); })
      .then(function () { return self.skipWaiting(); })
  );
});

self.addEventListener("activate", function (event) {
  event.waitUntil(
    caches.keys()
      .then(function (keys) {
        return Promise.all(keys.filter(function (k) {
          return k.indexOf("dansal-") === 0 && k !== CACHE;
        }).map(function (k) { return caches.delete(k); }));
      })
      .then(function () { return self.clients.claim(); })
  );
});

self.addEventListener("fetch", function (event) {
  var req = event.request;
  if (req.method !== "GET") return;
  var url = new URL(req.url);
  if (url.origin !== self.location.origin) return;
  for (var i = 0; i < BYPASS.length; i++) {
    if (url.pathname.indexOf(BYPASS[i]) === 0) return;
  }

  if (req.mode === "navigate") {
    event.respondWith(fetch(req).catch(function () {
      return caches.match("/offline");
    }));
    return;
  }

  if (url.pathname.indexOf("/static/") === 0 || PRECACHE.indexOf(url.pathname) !== -1) {
    event.respondWith(caches.open(CACHE).then(function (cache) {
      return cache.match(req).then(function (hit) {
        if (hit) return hit;
        return fetch(req).then(function (res) {
          if (res.ok) cache.put(req, res.clone());
          return res;
        });
      });
    }));
  }
});
