import { ipcMain } from "electron";
import path from "node:path";
import { nodeCliLinkIo } from "./cli-link-fs";
import { CLI_LINK_DIRECTORY, inspectCliLink, linkCli, unlinkCli, type CliLinkRequest } from "../shared/cli-link";
import { bundledDaemonBinaryName } from "../shared/daemon-launch";

let developerMode = false;

export function setCliLinkDeveloperMode(enabled: boolean): void {
	developerMode = enabled;
}

export function registerCliLinkIpc(input: {
	isPackaged: () => boolean;
	resourcesPath: string;
	appImage: string | undefined;
	dataDir: string;
	appVersion: string;
	shellPath: () => string | undefined;
}): void {
	const request = (confirmReplacement = false, expectedTarget?: string | null): CliLinkRequest => ({
		developerMode,
		platform: process.platform,
		isPackaged: input.isPackaged(),
		appImage: input.appImage,
		appCliPath: path.join(input.resourcesPath, "daemon", bundledDaemonBinaryName(process.platform)),
		linkDirectory: CLI_LINK_DIRECTORY,
		dataDir: input.dataDir,
		appVersion: input.appVersion,
		shellPath: input.shellPath(),
		allowSystemLinkDir: true,
		confirmReplacement,
		expectedTarget,
	});
	const io = nodeCliLinkIo();
	ipcMain.handle("cliLink:setEnabled", (_event, enabled: unknown) => {
		setCliLinkDeveloperMode(enabled === true);
	});
	ipcMain.handle("cliLink:inspect", () => inspectCliLink(io, request()));
	ipcMain.handle("cliLink:link", (_event, confirm: unknown, target: unknown) => linkCli(io, request(confirm === true, typeof target === "string" || target === null ? target : undefined)));
	ipcMain.handle("cliLink:unlink", (_event, confirm: unknown, target: unknown) => unlinkCli(io, request(confirm === true, typeof target === "string" || target === null ? target : undefined)));
}
