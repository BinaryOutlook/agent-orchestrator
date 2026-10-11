import { useLayoutEffect, useRef, useState } from "react";
import { workerProjectForEnvironment } from "./worker-controls";

/** Apply selections on tap; only search waits for the native sheet to dismiss. */
export function useWorkerControlsDraft({ open, selectedEnvironmentId, selectedProjectId, projects, onSelectEnvironment, onSelectProject, onDismiss, onSearch }: {
	open: boolean;
	selectedEnvironmentId: string;
	selectedProjectId: string;
	projects: { id: string; name: string; hostId?: string; hostName?: string }[];
	onSelectEnvironment(id: string): void;
	onSelectProject(id: string): void;
	onDismiss(): void;
	onSearch(): void;
}) {
	const [environmentId, setEnvironmentId] = useState(selectedEnvironmentId);
	const [projectId, setProjectId] = useState(selectedProjectId);
	const wasOpen = useRef(false);
	const closed = useRef(!open);
	const searchAfterClose = useRef(false);
	useLayoutEffect(() => {
		if (open && !wasOpen.current) {
			setEnvironmentId(selectedEnvironmentId);
			setProjectId(selectedProjectId);
			closed.current = false;
			searchAfterClose.current = false;
		}
		wasOpen.current = open;
	}, [open, selectedEnvironmentId, selectedProjectId]);
	return {
		environmentId,
		projectId,
		selectEnvironment(id: string) {
			const nextProjectId = workerProjectForEnvironment(projects, id, projectId);
			setEnvironmentId(id);
			setProjectId(nextProjectId);
			if (id !== selectedEnvironmentId) onSelectEnvironment(id);
			if (nextProjectId !== selectedProjectId) onSelectProject(nextProjectId);
			onDismiss();
		},
		selectProject(id: string) {
			setProjectId(id);
			if (id !== selectedProjectId) onSelectProject(id);
			onDismiss();
		},
		close: onDismiss,
		search() { searchAfterClose.current = true; onDismiss(); },
		didClose() {
			if (closed.current) return;
			closed.current = true;
			// Native onClose fires after SwiftUI dismissal / Android hide completes.
			onDismiss();
			if (searchAfterClose.current) onSearch();
		},
	};
}
