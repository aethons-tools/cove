import { afterEach, describe, expect, it, vi } from "vitest";
import { render } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { setLiveSource, useLiveEvents } from "./useLiveEvents";

class FakeEventSource {
  static last: FakeEventSource | undefined;
  listeners: Record<string, (() => void)[]> = {};
  closed = false;
  constructor(public url: string) { FakeEventSource.last = this; }
  addEventListener(ev: string, fn: () => void) { (this.listeners[ev] ??= []).push(fn); }
  close() { this.closed = true; }
  emit(ev: string) { this.listeners[ev]?.forEach((fn) => fn()); }
}

function Probe() { useLiveEvents(); return null; }

function mount() {
  const client = new QueryClient();
  const spy = vi.spyOn(client, "invalidateQueries");
  const r = render(<QueryClientProvider client={client}><Probe /></QueryClientProvider>);
  return { spy, ...r };
}

afterEach(() => { setLiveSource(); vi.unstubAllGlobals(); FakeEventSource.last = undefined; });

describe("useLiveEvents", () => {
  it("maps changed and presence to invalidations, and closes on unmount", () => {
    vi.stubGlobal("EventSource", FakeEventSource);
    const { spy, unmount } = mount();
    const es = FakeEventSource.last!;
    expect(es.url).toBe("/app/user/events");
    es.emit("changed");
    expect(spy.mock.calls.map((c) => c[0]?.queryKey)).toEqual([["rail"], ["conversation"], ["presence"], ["session"]]);
    spy.mockClear();
    es.emit("presence");
    expect(spy.mock.calls.map((c) => c[0]?.queryKey)).toEqual([["presence"], ["session"]]);
    unmount();
    expect(es.closed).toBe(true);
  });

  it("catches up after a reconnect (changes may have been missed), not on the first open", () => {
    vi.stubGlobal("EventSource", FakeEventSource);
    const { spy } = mount();
    const es = FakeEventSource.last!;
    es.emit("open");
    expect(spy).not.toHaveBeenCalled();
    es.emit("open");
    expect(spy.mock.calls.map((c) => c[0]?.queryKey)).toEqual([["rail"], ["conversation"], ["presence"], ["session"]]);
  });

  it("does nothing without EventSource (the 30 s poll covers it)", () => {
    vi.stubGlobal("EventSource", undefined);
    expect(() => mount()).not.toThrow();
  });

  it("a presence push spares the Artifacts tab", () => {
    vi.stubGlobal("EventSource", FakeEventSource);
    const { spy } = mount();
    FakeEventSource.last!.emit("presence");
    const keep = spy.mock.calls[1]![0]!.predicate as unknown as (q: { queryKey: unknown[] }) => boolean;
    expect(keep({ queryKey: ["session", "ses_1"] })).toBe(true);
    expect(keep({ queryKey: ["session", "ses_1", "pane", "status"] })).toBe(true);
    expect(keep({ queryKey: ["session", "ses_1", "pane", "artifacts"] })).toBe(false);
  });

  it("re-dispatches board signals as jam-board window events", () => {
    vi.stubGlobal("EventSource", FakeEventSource);
    mount();
    const got: string[] = [];
    const on = (e: Event) => got.push((e as CustomEvent<string>).detail);
    window.addEventListener("jam-board", on);
    FakeEventSource.last!.listeners.board?.forEach((fn) => (fn as unknown as (e: MessageEvent) => void)(new MessageEvent("board", { data: "brd_1" })));
    window.removeEventListener("jam-board", on);
    expect(got).toEqual(["brd_1"]);
  });

  it("a custom source drives the same invalidations", () => {
    let emit: (e: string, d: string) => void = () => {};
    const unsub = vi.fn();
    setLiveSource((on) => { emit = on; return unsub; });
    const { spy, unmount } = mount();
    const keys = () => spy.mock.calls.map((c) => c[0]?.queryKey);
    const changedSet = [["rail"], ["conversation"], ["presence"], ["session"]];
    emit("open", "");
    expect(spy).not.toHaveBeenCalled();
    emit("changed", "");
    expect(keys()).toEqual(changedSet);
    spy.mockClear();
    emit("presence", "");
    expect(keys()).toEqual([["presence"], ["session"]]);
    spy.mockClear();
    emit("open", "");
    expect(keys()).toEqual(changedSet);
    const got: string[] = [];
    const on = (e: Event) => got.push((e as CustomEvent<string>).detail);
    window.addEventListener("jam-board", on);
    emit("board", "brd_9");
    window.removeEventListener("jam-board", on);
    expect(got).toEqual(["brd_9"]);
    unmount();
    expect(unsub).toHaveBeenCalledTimes(1);
  });
});
