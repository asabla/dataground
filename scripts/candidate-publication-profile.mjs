const profiles = Object.freeze({
  codex: Object.freeze({
    imageRepository: "ghcr.io/asabla/dataground-codex-candidate",
    workflow: ".github/workflows/codex-compatibility.yml",
    buildJob: "native-sandbox",
    architectures: Object.freeze(["amd64", "arm64"]),
    user: "sandbox",
    labels: Object.freeze({
      "dataground.dev.codex-compatibility-source": "4c70bff480af37b1bf1a9b352b8341060fe55755",
    }),
  }),
  supervisor: Object.freeze({
    imageRepository: "ghcr.io/asabla/dataground-supervisor-candidate",
    workflow: ".github/workflows/openshell-supervisor-compatibility.yml",
    buildJob: "strict-landlock",
    architectures: Object.freeze(["arm64"]),
    user: "",
    labels: Object.freeze({
      "dataground.dev.supervisor-compatibility-source": "d556748771c41cbbd4e4dd7cd9030c798afe2b7d",
      "dataground.dev.supervisor-compatibility-patch":
        "5e97724dd9d9e7fad9abed8a46b9a4d6e06979119998c411daf34b2423056057",
    }),
  }),
});

export function candidateProfile(candidate) {
  if (!Object.hasOwn(profiles, candidate)) throw new Error("Unknown candidate profile.");
  return profiles[candidate];
}

export function candidateImageConfiguration(config, candidate) {
  const profile = candidateProfile(candidate);
  if (!config || typeof config !== "object" || Array.isArray(config)) return false;
  // Containerd omits an unset User. Explicit null or a named replacement is
  // different metadata; the Codex workload image must always name sandbox.
  const user = Object.hasOwn(config, "User") ? config.User : "";
  return (
    user === profile.user &&
    config.Labels?.["org.opencontainers.image.source"] === "https://github.com/asabla/dataground" &&
    config.Labels?.["dataground.dev.certification-eligible"] === "false" &&
    Object.entries(profile.labels).every(([key, value]) => config.Labels?.[key] === value)
  );
}
