import { createContext } from "react";

// Controls subscribe by their stable Field/Row id. Maps also keep fixed ids
// such as "constructor" from accidentally reading an inherited property.
export const FormErrorsContext = createContext<ReadonlyMap<string, string>>(new Map());
