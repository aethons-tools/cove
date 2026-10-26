import { afterEach, describe, expect, it, vi } from "vitest";
import { render } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { useLiveEvents } from "./useLiveEvents";

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

afterEach(() => { vi.unstubAllGlobals(); FakeEventSource.last = undefined; });

describe("useLiveEvents", () => {
  it("maps changed and presence to invalidations, and closes on unmount", () => {
    vi.stubGlobal("EventSource", FakeEventSource);
    const { spy, unmount } = mount();
    const es = FakeEventSource.last!;
    expect(es.url).toBe("/app/user/events");
    es.emit("changed");
    expect(spy.mock.calls.map((c) => c[0]?.queryKey)).toEqual([["rail"], ["conversation"], ["presence"]]);
    spy.mockClear();
    es.emit("presence");
    expect(spy.mock.calls.map((c) => c[0]?.queryKey)).toEqual([["presence"]]);
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
    expect(spy.mock.calls.map((c) => c[0]?.queryKey)).toEqual([["rail"], ["conversation"], ["presence"]]);
  });

  it("does nothing without EventSource (the 30 s poll covers it)", () => {
    vi.stubGlobal("EventSource", undefined);
    expect(() => mount()).not.toThrow();
  });
});
