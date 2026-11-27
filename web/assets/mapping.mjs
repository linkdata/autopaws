export function createMappingControls({get, send, discard}) {
  let state = null;
  const inform = text => { get("mapping-instructions").textContent = text; };
  const integer = id => {
    const text = get(id).value.trim();
    const value = Number(text);
    if (!text || !Number.isSafeInteger(value)) throw new Error("Enter whole numbers for every geometry field.");
    return value;
  };
  const control = event => {
    discard();
    if (!send(event)) inform("Reconnect before changing pointer settings.");
  };
  get("mapping-reset").addEventListener("click", () => control({type: "mapping_reset"}));
  get("mapping-apply").addEventListener("click", () => {
    try {
      if (get("mapping-mode").value === "legacy") {
        control({type: "mapping_set", mapping: {mode: "legacy"}});
        return;
      }
      const geometry = {
        offset: {x: integer("mapping-offset-x"), y: integer("mapping-offset-y")},
        width: integer("mapping-width"), height: integer("mapping-height"),
        span: {x: integer("mapping-span-x"), y: integer("mapping-span-y"),
          width: integer("mapping-span-width"), height: integer("mapping-span-height")},
      };
      if (Math.min(geometry.width, geometry.height, geometry.span.width, geometry.span.height) < 2) {
        throw new Error("Enter display and input span sizes of at least two pixels.");
      }
      control({type: "mapping_set", mapping: {geometry}});
    } catch (error) { inform(error.message); }
  });
  return {
    configure(value) {
      if (!value || ![value.revision, value.frame_generation, value.after_sequence].every(token =>
        typeof token === "string" && /^[0-9]+$/.test(token)) || typeof value.ready !== "boolean" ||
        !["legacy", "extended"].includes(value.mode)) throw new Error("Invalid pointer configuration");
      const changed = state?.revision !== value.revision || state?.mode !== value.mode ||
        JSON.stringify(state?.geometry) !== JSON.stringify(value.geometry);
      state = value;
      get("mapping-status").textContent = [state.mode === "legacy" ? "Identity mapping" : "Extended display",
        state.ready ? "ready" : "geometry required", state.message ?? "", state.error ?? ""].filter(Boolean).join(" · ");
      if (!changed) return;
      get("mapping-mode").value = state.mode;
      for (const [id, value] of Object.entries({
        "offset-x": state.geometry?.offset?.x ?? 0, "offset-y": state.geometry?.offset?.y ?? 0,
        width: state.geometry?.width, height: state.geometry?.height,
        "span-x": state.geometry?.span?.x ?? 0, "span-y": state.geometry?.span?.y ?? 0,
        "span-width": state.geometry?.span?.width, "span-height": state.geometry?.span?.height,
      })) get(`mapping-${id}`).value = value === undefined ? "" : String(value);
    },
    reset() { state = null; },
  };
}
