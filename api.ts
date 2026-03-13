export class WSBackend {
	private ws: WebSocket | null = null;
	private tokenProvider?: () => string;
	private url: string;
	private callbacks: Record<string, {
		callback: (err: any, res: any) => void;
		timeout: ReturnType<typeof setTimeout>;
	}> = {};
	private counter = 0;
	private _api: any = null;
	private queue: Array<{ id: string; method: string; params: any[]; resolve: Function; reject: Function }> = [];
	private reconnectDelay = 1000;
	private _connected = false;
	private connectedListeners: Array<() => void> = [];
	private disconnectedListeners: Array<() => void> = [];
	private callTimeout = 10000; // 10 second timeout for calls

	constructor(url: string) {
		this.url = url;
		this._api = new Proxy({}, {
			get: (_t, method: string) => (...args: any[]) => this.call(method, args)
		});
		this.connect();
	}

	public setTokenProvider(fn: () => string) {
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
			let msg: any;
			try {
				msg = JSON.parse(evt.data);
			} catch {
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
		};

		this.ws.onclose = () => {
			const wasConnected = this._connected;
			this._connected = false;

			Object.keys(this.callbacks).forEach(id => {
				const callbackData = this.callbacks[id];
				clearTimeout(callbackData.timeout);
				callbackData.callback({ message: 'WebSocket disconnected' }, null);
				delete this.callbacks[id];
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

	private call(method: string, params: any[]): Promise<any> {
		const id = (++this.counter).toString();

		return new Promise((resolve) => {
			const timeout = setTimeout(() => {
				if (this.callbacks[id]) {
					console.warn(`[WS] Call timeout for \${method} (id: \${id})`);
					this.callbacks[id].callback({ message: 'Request timeout' }, null);
					delete this.callbacks[id];
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
	}

	private _send(id: string, method: string, params: any[]) {
		if (!this.ws) throw new Error("WebSocket not initialized");
		const msg: any = { id, method, params };
		if (this.tokenProvider) msg.token = this.tokenProvider();
		this.ws.send(JSON.stringify(msg));
	}
}
