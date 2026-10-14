import { useEffect } from "react";
import { aoBridge } from "../lib/bridge";
import { useCloudSession } from "../lib/cloud-session";
import { clearCloudUser, identifyCloudUser, retireBootstrapId } from "../lib/telemetry";

// Ties the AO Cloud sign-in to the PostHog identity: identifies the user in the
// renderer (email as a person property, once) and hands the opaque user ID to the
// daemon through main so its events join the same person. Only the production
// WorkOS account is identified; the dev-only local provider never is.
export function TelemetryIdentityRuntime() {
	const { session, status } = useCloudSession();
	const userId = session?.authProvider === "workos" ? session.user.id : null;
	const email = session?.authProvider === "workos" ? session.user.email : "";
	useEffect(() => {
		// Until the session resolves, "no user" only means "not known yet"; telling
		// the daemon would be read as a sign-out and retire the install ID.
		if (status === "loading") return;
		const handoff = aoBridge.telemetry.setCloudUser(userId);
		if (userId) {
			void identifyCloudUser({ id: userId, email });
			handoff.catch(() => undefined);
			return;
		}
		clearCloudUser();
		// Main knows whether the install ID this launch started with was linked to
		// an account that has since left, including one from an earlier launch.
		handoff.then((retired) => retired && retireBootstrapId()).catch(() => undefined);
	}, [userId, email, status]);
	return null;
}
