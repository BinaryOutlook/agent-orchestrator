import { useEffect } from "react";
import { aoBridge } from "../lib/bridge";
import { useCloudSession } from "../lib/cloud-session";
import { clearCloudUser, identifyCloudUser } from "../lib/telemetry";

// Waits between attempts to get the daemon to record the signed-in user. The
// daemon is usually still starting when the session resolves; after the last
// attempt this launch stays anonymous rather than identify without a record.
export const BINDING_RETRY_MS = [1_000, 3_000, 10_000, 30_000, 60_000, 180_000];

// Ties the AO Cloud sign-in to the PostHog identity: hands the opaque user ID to
// the daemon through main so its events join the same person, and identifies the
// user in the renderer (email as a person property, once) only after the daemon
// acknowledged it. identify() links the install ID to the account permanently;
// without the daemon's record of that link a later sign-out could not retire the
// ID. Only the production WorkOS account is identified; the dev-only local
// provider never is.
export function TelemetryIdentityRuntime() {
	const { session, status } = useCloudSession();
	const userId = session?.authProvider === "workos" ? session.user.id : null;
	const email = session?.authProvider === "workos" ? session.user.email : "";
	useEffect(() => {
		// Until the session resolves, "no user" only means "not known yet"; telling
		// the daemon would be read as a sign-out and retire the install ID.
		if (status === "loading") return;
		if (!userId) {
			void Promise.resolve(aoBridge.telemetry.setCloudUser(null)).catch(() => undefined);
			clearCloudUser();
			return;
		}
		let cancelled = false;
		void (async () => {
			for (const delay of [0, ...BINDING_RETRY_MS]) {
				if (delay) await new Promise((resolve) => setTimeout(resolve, delay));
				if (cancelled) return;
				const acknowledged = await Promise.resolve(aoBridge.telemetry.setCloudUser(userId)).catch(() => false);
				if (cancelled) return;
				if (acknowledged) {
					await identifyCloudUser({ id: userId, email });
					return;
				}
			}
		})();
		return () => {
			cancelled = true;
		};
	}, [userId, email, status]);
	return null;
}
