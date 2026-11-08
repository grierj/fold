// The digest tests compare against Node's SHA-256. The web project carries no
// @types/node, so this declares the two functions they use.
declare module "node:crypto" {
  interface Hash {
    update(data: Uint8Array): Hash;
    digest(): Uint8Array;
    digest(encoding: "hex"): string;
  }
  export function createHash(algorithm: "sha256"): Hash;
  export function randomBytes(size: number): Uint8Array;
}
