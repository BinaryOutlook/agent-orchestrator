import { existsSync, readFileSync } from "node:fs";
import { mkdir, rm, writeFile } from "node:fs/promises";
import path from "node:path";

/** Same file the daemon's telemetry Identity reads (adapters/telemetry/identity.go). */
export const TELEMETRY_OPT_OUT_FILE = "telemetry_opt_out";
const INSTALL_ID_FILE = "telemetry_install_id";
/** Written by the daemon while the install ID may be linked to an AO Cloud account. */
const BOUND_USER_FILE = "telemetry_bound_user";

type Fetcher = (input: string, init: RequestInit) => Promise<Response>;

const CLOUD_USER_ID_PATTERN = /^[A-Za-z0-9_-]{1,64}$/;

/**
 * Desktop side of the PostHog identity: the opt-out switch (a marker file the
 * daemon also reads) and the loopback hand-off of the signed-in AO Cloud user ID
 * to the daemon. The email never goes through here; it is set by the renderer at
 * identify time and the daemon never sees it.
 */
export class TelemetryIdentityController {
	private cloudUserId: string | null = null;
	private pushed = true;
	// Bumped whenever the daemon's copy goes stale (restart, or a new user), so an
	// in-flight hand-off to the previous daemon cannot mark the new one as synced.
	private generation = 0;
	// The install ID handed to the renderer's PostHog client at startup.
	private servedInstallId: string | null = null;

	constructor(
		private readonly dataDir: string,
		private readonly origin: () => string | null,
		private readonly fetcher: Fetcher = fetch,
		private readonly broadcast: (optedOut: boolean) => void = () => undefined,
	) {}

	isOptedOut(): boolean {
		return existsSync(path.join(this.dataDir, TELEMETRY_OPT_OUT_FILE));
	}

	async setOptedOut(optedOut: boolean): Promise<boolean> {
		const file = path.join(this.dataDir, TELEMETRY_OPT_OUT_FILE);
		if (optedOut) {
			await mkdir(this.dataDir, { recursive: true });
			await writeFile(file, "1\n", { mode: 0o600 });
		} else {
			await rm(file, { force: true });
		}
		this.broadcast(optedOut);
		return optedOut;
	}

	/** Remembers the install ID the renderer's PostHog client was created with. */
	noteServedInstallId(id: string | null): void {
		this.servedInstallId = id;
	}

	/**
	 * Whether the install ID the renderer holds is linked to an account that is no
	 * longer signed in: the daemon still flags it bound (rotation pending, e.g. the
	 * daemon is not ready yet) or has already replaced it on disk.
	 */
	private servedInstallIdRetired(): boolean {
		if (existsSync(path.join(this.dataDir, BOUND_USER_FILE))) return true;
		try {
			const current = readFileSync(path.join(this.dataDir, INSTALL_ID_FILE), "utf8").trim();
			return this.servedInstallId !== null && current !== "" && current !== this.servedInstallId;
		} catch {
			return false;
		}
	}

	/**
	 * Records the signed-in user (null on sign-out) and pushes it to the daemon.
	 * Resolves true when this was a sign-out and the install ID the renderer is
	 * using belongs to the account that just left, so the renderer must drop it.
	 */
	async setCloudUser(userId: string | null): Promise<boolean> {
		if (userId !== null && !CLOUD_USER_ID_PATTERN.test(userId)) throw new Error("invalid cloud user id");
		this.cloudUserId = userId;
		this.pushed = false;
		this.generation++;
		await this.flush();
		return userId === null && this.servedInstallIdRetired();
	}

	/**
	 * Marks the daemon's copy stale. Called whenever the daemon leaves `ready`: a
	 * restarted daemon (even on the same port) starts with an empty identity, so
	 * the next ready transition must replay the signed-in user.
	 */
	invalidate(): void {
		this.pushed = false;
		this.generation++;
	}

	/** Retries the hand-off; called when the daemon becomes ready. */
	async flush(): Promise<void> {
		if (this.pushed) return;
		const base = this.origin();
		if (!base) return;
		const sent = this.cloudUserId ?? "";
		const generation = this.generation;
		try {
			const response = await this.request(base, "/internal/telemetry/identity", { method: "POST", body: { cloudUserId: sent } });
			// An older daemon without the route answers 404/405; nothing to retry.
			if (generation !== this.generation) return;
			this.pushed = response.ok || response.status === 404 || response.status === 405;
		} catch {
			// Daemon not reachable yet; flush() runs again on the next ready transition.
		}
	}

	/** The GitHub login the daemon resolved from the authenticated account, or null. */
	async githubLogin(): Promise<string | null> {
		const base = this.origin();
		if (!base) return null;
		try {
			const response = await this.request(base, "/api/v1/telemetry/identity", { method: "GET" });
			if (!response.ok) return null;
			const body = (await response.json()) as { githubLogin?: unknown };
			return typeof body.githubLogin === "string" && body.githubLogin !== "" ? body.githubLogin : null;
		} catch {
			return null;
		}
	}

	private async request(base: string, pathname: string, init: { method: string; body?: object }): Promise<Response> {
		const parsed = new URL(base);
		if (parsed.protocol !== "http:" || parsed.hostname !== "127.0.0.1" || parsed.username || parsed.password || parsed.pathname !== "/" || parsed.search || parsed.hash) {
			throw new Error("daemon telemetry identity origin must be exact loopback HTTP");
		}
		const controller = new AbortController();
		const timer = setTimeout(() => controller.abort(), 2_000);
		try {
			return await this.fetcher(`${parsed.origin}${pathname}`, {
				method: init.method,
				signal: controller.signal,
				headers: init.body ? { "content-type": "application/json" } : undefined,
				body: init.body ? JSON.stringify(init.body) : undefined,
			});
		} finally {
			clearTimeout(timer);
		}
	}
}
