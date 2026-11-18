import { boolean, function_, is, number, object, string, type InferOutput } from "valibot";

const flag = boolean();
const callable = function_();
const numeric = number();
const dictionary = object({});
const text = string();

export function isBoolean(value: unknown): value is boolean {
  return is(flag, value);
}

export function isCallable(value: unknown): value is InferOutput<typeof callable> {
  return is(callable, value);
}

export function isNumber(value: unknown): value is number {
  return is(numeric, value);
}

// Arrays pass this check. JSON dictionary readers use isJsonObject.
export function isObject(value: unknown): value is object {
  return is(dictionary, value);
}

export function isString(value: unknown): value is string {
  return is(text, value);
}

export type JsonValue = null | boolean | number | string | readonly JsonValue[] | JsonObject;

export interface JsonObject {
  [key: string]: JsonValue | undefined;
}

export function isJsonObject(value: JsonValue | undefined): value is JsonObject {
  return !Array.isArray(value) && is(dictionary, value);
}
