// A window Litmus launched to test a pull request names itself after the build
// it shows, so the two builds under test read as "Litmus: PR build #6572" and
// "Litmus: main for #6572" in the Dock and the title bar, never as the user's
// own Agent Orchestrator. Litmus sets LITMUS_LEG (head or base) and LITMUS_PR.
export interface LitmusLabel {
	/** The app name (Dock menu, About panel). */
	name: string;
	/** The main window title. */
	title: string;
}

export function litmusLabel(env: NodeJS.ProcessEnv): LitmusLabel | undefined {
	const leg = env.LITMUS_LEG?.trim();
	if (leg !== "head" && leg !== "base") return undefined;
	const pr = env.LITMUS_PR?.trim();
	const number = pr && /^\d+$/.test(pr) ? ` #${pr}` : "";
	const title = leg === "head" ? `Litmus: PR build${number}` : `Litmus: main${number ? " for" + number : ""}`;
	return { name: title, title };
}
