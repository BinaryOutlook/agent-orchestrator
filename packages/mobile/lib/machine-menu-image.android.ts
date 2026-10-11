import type { MachineSymbol } from "./machineAppearance";

// Android native menus require bundled drawables, not SF Symbol names.
const images: Record<MachineSymbol, number> = {
	server: require("../assets/icons/machine-server.xml"),
	cloud: require("../assets/icons/machine-cloud.xml"),
	linux: require("../assets/icons/machine-linux.xml"),
	desktop: require("../assets/icons/machine-desktop.xml"),
	laptop: require("../assets/icons/machine-laptop.xml"),
	"mac-mini": require("../assets/icons/machine-mini.xml"),
	"mac-studio": require("../assets/icons/machine-workstation.xml"),
};

export function machineMenuImage(symbol: MachineSymbol): number {
	return images[symbol];
}
