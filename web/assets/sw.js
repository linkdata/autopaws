self.addEventListener("install", () => self.skipWaiting());
self.addEventListener("activate", event => event.waitUntil(self.clients.claim()));
// The KVM must stay online; never cache video, control requests or assets.
self.addEventListener("fetch", event => event.respondWith(fetch(event.request)));
