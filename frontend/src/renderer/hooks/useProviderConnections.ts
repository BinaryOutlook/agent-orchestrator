/**
 * Lists the signed-in user's coding-agent connections (GET /me/providers).
 * Cloud agent credentials are personal: each user logs in to their harnesses
 * from the Harnesses settings page, and the connection runs their cloud
 * sessions in every org they belong to.
 */

import { useQuery } from "@tanstack/react-query";
import type { CloudCpClient, CloudCpProviderConnection } from "../lib/cloud-cp";
import { CloudCpError } from "../lib/cloud-cp/errors";
import { CLOUD_AGENT_PROVIDERS } from "../lib/cloud-agents";
import { useCloudCp } from "./useCloudCp";
import { useCloudOrg } from "./useCloudOrg";

export const providerConnectionsQueryKey = ["cloud-provider-connections"] as const;
const noChatModels: string[] = [];

export function useProviderConnections() {
	const { client, ready } = useCloudCp();
	const { org } = useCloudOrg();
	const query = useQuery({
		queryKey: [...providerConnectionsQueryKey, org?.id ?? ""],
		enabled: ready,
		staleTime: 60_000,
		queryFn: () => loadProviderConnections(client, org?.id),
	});
	return { ...query, data: query.data?.connections, chatSessionModels: query.data?.chatSessionModels ?? noChatModels };
}

export async function loadProviderConnections(client: CloudCpClient, orgId?: string): Promise<{
	connections: CloudCpProviderConnection[];
	chatSessionModels: string[];
}> {
	try {
		const response = await client.listUserProviderConnections();
		return { connections: response.providerConnections, chatSessionModels: response.chatSessionModels ?? [] };
	} catch (error) {
		if (!(error instanceof CloudCpError) || error.status !== 404 || !orgId) throw error;
		const response = await client.listOrgProviderConnections(orgId);
		return { connections: response.providerConnections, chatSessionModels: response.chatSessionModels ?? [] };
	}
}

/**
 * True when at least one coding-agent connection the control plane validated
 * exists. The personal list also holds non-agent credentials (a GitHub token),
 * which do not count.
 */
export function hasValidAgentConnection(connections: CloudCpProviderConnection[] | undefined): boolean {
	return (connections ?? []).some(
		(connection) =>
			connection.validationState === "valid" &&
			(CLOUD_AGENT_PROVIDERS as readonly string[]).includes(connection.provider),
	);
}
