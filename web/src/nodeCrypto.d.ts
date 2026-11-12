// The digest tests compare against Node's SHA-256, and the seeded suites record
// the seeds they ran through the filesystem. The web project carries no
// @types/node, so this declares the little they use: two crypto functions, one
// fs function and the env record vitest provides.
declare module "node:crypto" {
  interface Hash {
    update(data: Uint8Array | string): Hash;
    digest(): Uint8Array;
    digest(encoding: "hex"): string;
  }
  export function createHash(algorithm: "sha256"): Hash;
  export function randomBytes(size: number): Uint8Array;
}

declare module "node:fs" {
  export function appendFileSync(path: string, data: string): void;
}

declare module "node:process" {
  export const env: Record<string, string | undefined>;
}
