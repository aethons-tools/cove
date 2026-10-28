// Cancelled is a write the user called off (say, at "Discard your unsaved
// edits?"): the forms and actions drop it silently.
export class Cancelled extends Error {
  constructor() {
    super("cancelled");
    this.name = "Cancelled";
  }
}
