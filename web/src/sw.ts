// Fold's service worker (M3.13c1, research 2.3), built to /sw.js and
// registered only by "Turn on notifications". The handlers are in
// sw/wire.ts: install, activate, push, notificationclick and
// pushsubscriptionchange, with no fetch handler and no cache.
import { wire, type WorkerScope } from "./sw/wire";

wire(globalThis as unknown as WorkerScope);
