/// <reference types="vite/client" />

// @plantuml/core ships no types. The engine's surface is typed where it is
// used (plantuml.ts); themes.js only sets a global.
declare module "@plantuml/core";
declare module "@plantuml/core/themes.js";
