export class WSBackend {
	private ws: WebSocket | null = null;
	private tokenProvider?: () => string | Promise<string>;
	private url: string;
	private callbacks: Record<string, {
		callback: (err: any, res: any) => void;
		timeout: ReturnType<typeof setTimeout>;
	}> = {};
	private streamCallbacks: Record<string, {
		onData: (data: any) => void;
		onDone: (err?: any) => void;
	}> = {};
	private broadcastListeners: Map<string, Set<(data: any) => void>> = new Map();
	private counter = 0;
	private _api: any = null;
	private queue: Array<{ id: string; method: string; params: any[]; resolve: Function; reject: Function }> = [];
	private reconnectDelay = 1000;
	private _connected = false;
	private connectedListeners: Array<() => void> = [];
	private disconnectedListeners: Array<() => void> = [];
	private callTimeout = 10000;

	constructor(url: string) {
		this.url = url;
		this._api = new Proxy({}, {
			get: (_t, method: string) => (...args: any[]) => this.call(method, args)
		});
		this.connect();
	}

	public setTokenProvider(fn: () => string | Promise<string>) {
		this.tokenProvider = fn;
	}

	public get api() {
		return this._api;
	}

	public get connected() {
		return this._connected;
	}

	public onConnected(cb: () => void) {
		this.connectedListeners.push(cb);
	}

	public onDisconnected(cb: () => void) {
		this.disconnectedListeners.push(cb);
	}

	public setCallTimeout(ms: number) {
		this.callTimeout = ms;
	}

	public subscribe<K extends keyof BroadcastEvents>(topic: K, callback: (data: BroadcastEvents[K]) => void): () => void {
		const topicStr = String(topic);
		if (!this.broadcastListeners.has(topicStr)) {
			this.broadcastListeners.set(topicStr, new Set());
		}

		const cb = callback as (data: any) => void;
		this.broadcastListeners.get(topicStr)!.add(cb);

		return () => {
			this.broadcastListeners.get(topicStr)?.delete(cb);
		};
	}

	public stream<T = any>(method: string, params: any[] = []): AsyncIterable<T> & { cancel: () => void } {
		const callResult = this.call(method, params);
		return {
			cancel: () => {
				callResult[Symbol.asyncIterator]().return?.();
			},
			[Symbol.asyncIterator]() {
				return callResult[Symbol.asyncIterator]();
			}
		};
	}

	private connect() {
		this.ws = new WebSocket(this.url);

		this.ws.onopen = () => {
			console.log("[WS] Connected to", this.url);
			this._connected = true;
			this.connectedListeners.forEach(cb => cb());

			this.queue.forEach(item => {
				this._send(item.id, item.method, item.params);
			});
			this.queue = [];
		};

		this.ws.onmessage = (evt) => {
			const raw = typeof evt.data === "string" ? evt.data : "";
			if (!raw) return;
			try {
				const msg = JSON.parse(raw);
				this.handleMessage(msg);
			} catch (e) {
				console.warn("[WS] Failed to parse message:", e);
			}
		};

		this.ws.onclose = () => {
			const wasConnected = this._connected;
			this._connected = false;

			Object.keys(this.callbacks).forEach(id => {
				const callbackData = this.callbacks[id];
				if (callbackData) {
					clearTimeout(callbackData.timeout);
					callbackData.callback({ message: 'WebSocket disconnected' }, null);
					delete this.callbacks[id];
				}
			});

			Object.keys(this.streamCallbacks).forEach(id => {
				const streamData = this.streamCallbacks[id];
				if (streamData) {
					streamData.onDone({ message: 'WebSocket disconnected' });
					delete this.streamCallbacks[id];
				}
			});

			if (wasConnected) {
				this.disconnectedListeners.forEach(cb => cb());
			}

			console.log("[WS] Disconnected. Reconnecting...");
			setTimeout(() => this.connect(), this.reconnectDelay);
		};

		this.ws.onerror = (e) => {
			console.warn("[WS] Error:", e);
			this.ws?.close();
		};
	}

	private handleMessage(msg: any) {
		if (msg.topic) {
			const listeners = this.broadcastListeners.get(msg.topic);
			if (listeners) {
				listeners.forEach(cb => cb(msg.data));
			}
			return;
		}

		if (msg.stream) {
			if (this.callbacks[msg.id]) {
				clearTimeout(this.callbacks[msg.id].timeout);
				delete this.callbacks[msg.id];
			}
			const streamData = this.streamCallbacks[msg.id];
			if (!streamData) return;

			if (msg.done) {
				const err = (msg.result && typeof msg.result === "object") ? msg.result.error : msg.error;
				streamData.onDone(err);
				delete this.streamCallbacks[msg.id];
			} else {
				const data = (msg.result && typeof msg.result === "object" && "data" in msg.result)
					? msg.result.data
					: msg.result;
				streamData.onData(data);
			}
			return;
		}

		const callbackData = this.callbacks[msg.id];
		if (!callbackData) return;

		clearTimeout(callbackData.timeout);

		if (msg.result && typeof msg.result === "object" && ("data" in msg.result || "error" in msg.result)) {
			const r = msg.result;
			callbackData.callback(r.error, r.data);
		} else {
			callbackData.callback(msg.error, msg.result);
		}

		delete this.callbacks[msg.id];
		delete this.streamCallbacks[msg.id];
	}

	private call(method: string, params: any[]): Promise<any> & AsyncIterable<any> {
		const id = (++this.counter).toString();

		const streamQueue: Array<{ value?: any; done: boolean; error?: any }> = [];
		let resolveWaiting: ((val: IteratorResult<any>) => void) | null = null;
		let rejectWaiting: ((err: any) => void) | null = null;
		let isStreamDone = false;

		this.streamCallbacks[id] = {
			onData: (data: any) => {
				if (isStreamDone) return;
				if (resolveWaiting) {
					const r = resolveWaiting;
					resolveWaiting = null;
					rejectWaiting = null;
					r({ value: data, done: false });
				} else {
					streamQueue.push({ value: data, done: false });
				}
			},
			onDone: (err?: any) => {
				if (isStreamDone) return;
				isStreamDone = true;
				if (err) {
					if (rejectWaiting) {
						const r = rejectWaiting;
						resolveWaiting = null;
						rejectWaiting = null;
						r(err);
					} else {
						streamQueue.push({ done: true, error: err });
					}
				} else {
					if (resolveWaiting) {
						const r = resolveWaiting;
						resolveWaiting = null;
						rejectWaiting = null;
						r({ value: undefined, done: true });
					} else {
						streamQueue.push({ done: true });
					}
				}
			}
		};

		const promise = new Promise((resolve) => {
			const timeout = setTimeout(() => {
				if (this.callbacks[id]) {
					console.warn(`[WS] Call timeout for ${method} (id: ${id})`);
					this.callbacks[id].callback({ message: 'Request timeout' }, null);
					delete this.callbacks[id];
					delete this.streamCallbacks[id];
				}
			}, this.callTimeout);

			this.callbacks[id] = {
				callback: (err, res) => {
					resolve({ data: res, error: err });
				},
				timeout
			};

			if (this._connected && this.ws && this.ws.readyState === WebSocket.OPEN) {
				this._send(id, method, params);
			} else {
				console.log("[WS] Queueing call", method, id, params);
				this.queue.push({ id, method, params, resolve, reject: () => { } });
			}
		});

		const result: any = promise;
		const self = this;
		result[Symbol.asyncIterator] = () => {
			return {
				next: (): Promise<IteratorResult<any>> => {
					if (streamQueue.length > 0) {
						const item = streamQueue.shift()!;
						if (item.error) {
							return Promise.reject(item.error);
						}
						return Promise.resolve({ value: item.value, done: item.done });
					}
					if (isStreamDone) {
						return Promise.resolve({ value: undefined, done: true });
					}
					return new Promise<IteratorResult<any>>((res, rej) => {
						resolveWaiting = res;
						rejectWaiting = rej;
					});
				},
				return: (): Promise<IteratorResult<any>> => {
					isStreamDone = true;
					if (self.callbacks[id]) {
						clearTimeout(self.callbacks[id].timeout);
						delete self.callbacks[id];
					}
					delete self.streamCallbacks[id];
					if (self._connected && self.ws && self.ws.readyState === WebSocket.OPEN) {
						self.ws.send(JSON.stringify({ id, cancel: true }));
					}
					return Promise.resolve({ value: undefined, done: true });
				}
			};
		};

		return result;
	}

	private async _send(id: string, method: string, params: any[]) {
		if (!this.ws) throw new Error("WebSocket not initialized");
		const msg: any = { id, method, params };
		if (this.tokenProvider) msg.token = await this.tokenProvider();
		if (!this.ws || this.ws.readyState !== WebSocket.OPEN) {
			this.queue.push({ id, method, params, resolve: () => { }, reject: () => { } });
			return;
		}
		this.ws.send(JSON.stringify(msg));
	}
}
