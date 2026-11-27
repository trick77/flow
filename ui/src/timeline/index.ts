// The conversation timeline: folds an event stream (src/contract) into
// messages, activity folds, cards and dividers. Strings: timeline namespace.
// Owner: timeline area (docs/ownership.md).
export {
  foldEvents,
  type Folded,
  type QueuedMessage,
  type TimelineItem,
} from "./fold";
export { emptyStream, reduceStream, type StreamState } from "./state";
export { Timeline } from "./Timeline";
export { useSessionStream } from "./useSessionStream";
