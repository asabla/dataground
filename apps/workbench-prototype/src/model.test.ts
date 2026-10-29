import { describe, expect, it } from "vitest";
import { exampleQuery, notebookCode } from "./data";
import { initialModel, parseRoute, reducer } from "./model";

describe("fictional workflow boundaries", () => {
  it("keeps publication, route assignment and execution separate", () => {
    expect(reducer(initialModel, { type: "assign-route" })).toBe(initialModel);
    expect(reducer(initialModel, { type: "invoke" })).toBe(initialModel);
    const published = reducer(initialModel, { type: "publish" });
    expect(published.routeAssigned).toBe(false);
    expect(reducer(published, { type: "invoke" })).toBe(published);
    const routed = reducer(published, { type: "assign-route" });
    expect(reducer(routed, { type: "invoke" }).runStarted).toBe(true);
  });
  it("freezes an uncertain publication until its exact outcome is observed", () => {
    const uncertain = reducer(initialModel, { type: "publish", uncertain: true });
    expect(reducer(uncertain, { type: "publish" })).toBe(uncertain);
    expect(reducer(uncertain, { type: "agent-edit", name: "Changed", purpose: "Changed" })).toBe(
      uncertain,
    );
    expect(reducer(uncertain, { type: "assign-route" })).toBe(uncertain);
    expect(reducer(uncertain, { type: "publication-observe" }).publication).toBe("published");
  });
  it("requires an answer before approval and rejects expired answers", () => {
    expect(reducer(initialModel, { type: "approve" })).toBe(initialModel);
    expect(reducer(initialModel, { type: "answer", value: "All regions", expired: true })).toBe(
      initialModel,
    );
    expect(reducer(initialModel, { type: "answer", value: " " })).toBe(initialModel);
    const answered = reducer(initialModel, { type: "answer", value: "All regions" });
    expect(answered.run).toBe("approval");
    expect(reducer(answered, { type: "approve" }).run).toBe("succeeded");
    const denied = reducer(answered, { type: "deny" });
    expect(reducer(denied, { type: "approve" })).toBe(denied);
  });
  it("keeps requested cancellation separate from observed stop", () => {
    const cancelling = reducer(initialModel, { type: "cancel" });
    expect(cancelling.run).toBe("cancelling");
    expect(reducer(cancelling, { type: "answer", value: "All regions" })).toBe(cancelling);
    expect(reducer(cancelling, { type: "approve" })).toBe(cancelling);
    expect(reducer(cancelling, { type: "cancel-complete" }).run).toBe("cancelled");
  });
  it("does not present fixture results for arbitrary SQL or edited notebook code", () => {
    const edited = reducer(initialModel, { type: "query-edit", value: "DELETE FROM orders" });
    expect(reducer(edited, { type: "query-run" }).queryStatus).toBe("unsupported");
    const notebook = reducer(initialModel, { type: "notebook-edit", value: "print('changed')" });
    expect(reducer(notebook, { type: "notebook-run" }).notebookStatus).toBe("unsupported");
    expect(reducer(initialModel, { type: "query-run" }).queryStatus).toBe("running");
  });
  it("ignores a completion after cancellation or an intervening edit", () => {
    const running = reducer(initialModel, { type: "query-run" });
    const cancelled = reducer(running, { type: "query-cancel" });
    expect(reducer(cancelled, { type: "query-complete" })).toBe(cancelled);
    const edited = reducer(running, { type: "query-edit", value: "changed" });
    expect(reducer(edited, { type: "query-complete" })).toBe(edited);
    const completed = reducer(running, { type: "query-complete" });
    expect(reducer(completed, { type: "query-complete" }).queryRuns).toBe(1);
  });
  it("retains independent documents and resets all sample commands on scope change", () => {
    let state = reducer(initialModel, { type: "query-edit", value: "draft" });
    state = reducer(state, { type: "notebook-edit", value: "draft cell" });
    state = reducer(state, { type: "agent-new", name: "New service", purpose: "Analyze" });
    expect(state.query).toBe("draft");
    expect(state.notebook).toBe("draft cell");
    expect(reducer(state, { type: "reset" })).toEqual(initialModel);
    expect(initialModel.query).toBe(exampleQuery);
    expect(initialModel.notebook).toBe(notebookCode);
  });
  it("parses deep links without taking command contents from the URL", () => {
    expect(parseRoute("#/agents/revenue?tab=versions&prompt=ignored")).toEqual({
      area: "agents",
      id: "revenue",
      tab: "versions",
    });
    expect(parseRoute("#/unknown").area).toBe("home");
    expect(parseRoute("").area).toBe("home");
  });
});
