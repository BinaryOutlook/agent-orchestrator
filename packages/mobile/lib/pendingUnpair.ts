import AsyncStorage from "@react-native-async-storage/async-storage";
import * as SecureStore from "expo-secure-store";
import { unpairFromDaemon } from "./api";
import { configForEndpoint } from "./connect";
import { probeEndpoint } from "./connectRuntime";
import { findHost, type Host } from "./hosts";
import { raceEndpoints } from "./race";

// Only opaque job keys go in AsyncStorage. Each credential-bearing snapshot
// has its own SecureStore entry, avoiding a growing, size-limited secret blob.
const INDEX_KEY = "ao.pendingUnpairIds";
type PendingUnpair = { key: string; host: Host; installId: string; removed: boolean };
let storageTail: Promise<unknown> = Promise.resolve();

function mutate<T>(action: () => Promise<T>): Promise<T> {
	const result = storageTail.then(action, action);
	storageTail = result.catch(() => {});
	return result;
}

async function readKeys(): Promise<string[]> {
	const raw = await AsyncStorage.getItem(INDEX_KEY);
	return raw ? JSON.parse(raw) as string[] : [];
}

async function readJobs(): Promise<PendingUnpair[]> {
	const jobs = await Promise.all((await readKeys()).map(async (key) => {
		const raw = await SecureStore.getItemAsync(key);
		return raw ? JSON.parse(raw) as PendingUnpair : null;
	}));
	return jobs.filter((job): job is PendingUnpair => job !== null);
}

async function removeJob(key: string): Promise<void> {
	await mutate(async () => {
		const keys = await readKeys();
		await SecureStore.deleteItemAsync(key);
		const remaining = keys.filter((candidate) => candidate !== key);
		if (remaining.length) await AsyncStorage.setItem(INDEX_KEY, JSON.stringify(remaining));
		else await AsyncStorage.removeItem(INDEX_KEY);
	});
}

/** Persist first: losing this write must not throw away the only credential
 * with which the phone can revoke its remote notification registration. */
export async function queueMachineUnpair(host: Host, installId: string): Promise<void> {
	if (!host.id || !installId) throw new Error("Cannot safely queue machine unpairing without identity");
	await mutate(async () => {
		const keys = await readKeys();
		const key = `ao.pendingUnpair.${Date.now()}.${Math.random().toString(36).slice(2)}`;
		await SecureStore.setItemAsync(key, JSON.stringify({ key, host, installId, removed: false } satisfies PendingUnpair));
		try {
			await AsyncStorage.setItem(INDEX_KEY, JSON.stringify([...keys, key]));
		} catch (error) {
			await SecureStore.deleteItemAsync(key);
			throw error;
		}
	});
}

/** A durable marker distinguishes a deliberate re-pair from the brief window
 * between queuing cleanup and finishing local removal. */
export async function markMachineUnpaired(hostId: string): Promise<void> {
	await mutate(async () => {
		for (const job of await readJobs()) {
			if (job.host.id === hostId) await SecureStore.setItemAsync(job.key, JSON.stringify({ ...job, removed: true }));
		}
	});
}

/** Called through push's network mutation queue, so a re-pair registration
 * always runs after any already-started unpair, never before it. */
export async function flushMachineUnpairs(): Promise<void> {
	const jobs = await mutate(readJobs);
	await Promise.all(jobs.map(async (job) => {
		try {
			const paired = await findHost(job.host.id);
			if (paired) {
				if (job.removed) await removeJob(job.key); // Deliberately paired again.
				return;
			}
			const outcome = await raceEndpoints(job.host.endpoints, job.host.id,
				(endpoint, signal) => probeEndpoint(endpoint, signal, { retryTunnel: false }));
			if (!outcome.ok) return;
			// Pairing may complete while identity probes are in flight. An old
			// cleanup must not delete that new device registration.
			if (await findHost(job.host.id)) {
				await removeJob(job.key);
				return;
			}
			await unpairFromDaemon(configForEndpoint(outcome.endpoint, job.host.token, job.host.id), job.installId);
			await removeJob(job.key);
		} catch {
			// Keep the durable job on timeout, offline, auth or storage failure.
		}
	}));
}
