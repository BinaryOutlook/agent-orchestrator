import { createFileRoute } from "@tanstack/react-router";
import { LitmusView } from "../components/litmus/LitmusView";

export const Route = createFileRoute("/_shell/projects/$projectId_/litmus")({
	component: LitmusRoute,
});

function LitmusRoute() {
	const { projectId } = Route.useParams();
	return <LitmusView projectId={projectId} />;
}
