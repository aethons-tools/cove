// noun labels a count of n things: "member" for one, else "members" (or many).
export const noun = (n: number, one: string, many = one + "s") => (n === 1 ? one : many);
