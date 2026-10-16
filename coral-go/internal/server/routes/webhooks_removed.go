package routes

// Coral doesn't send webhooks (#3646, the operator's decision): an outbound request to a
// caller-chosen URL is attack surface, and the URL itself is usually a secret. A request that
// still names one is refused, so nobody thinks it applies.
const errWebhooksNotSupported = "webhooks are not supported"
