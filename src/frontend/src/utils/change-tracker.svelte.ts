export class ChangeTracker<T extends object> {
  private state: T;
  private proxy: T;

  // Rule IDs are unique within a parent, not across the whole configuration.
  private originalObjects = new Map<object, any>();
  private originalArrays = new WeakMap<object, any[]>();
  private snapshotTargets = new WeakMap<object, object>();
  private proxyCache = new WeakMap<object, any>();
  private reverseProxyCache = new WeakMap<object, object>();

  private dirtyObjectProps = new Map<object, Set<string>>();
  private dirtyArrays = new Set<object>();

  private version = $state(0);

  constructor(initialData: T) {
    this.state = $state(initialData);
    this.init(structuredClone(initialData));
    this.proxy = this.createProxy(this.state);
  }

  private init(snapshot: any) {
    this.originalObjects.clear();
    this.originalArrays = new WeakMap();
    this.snapshotTargets = new WeakMap();
    this.proxyCache = new WeakMap();
    this.reverseProxyCache = new WeakMap();
    this.dirtyObjectProps.clear();
    this.dirtyArrays.clear();
    this.indexAndLink(this.state, snapshot);
  }

  private indexAndLink(stateNode: any, snapshotNode: any) {
    if (!stateNode || typeof stateNode !== "object") return;
    stateNode = this.targetOf(stateNode);
    if (snapshotNode && typeof snapshotNode === "object") {
      this.snapshotTargets.set(snapshotNode, stateNode);
    }

    if (Array.isArray(stateNode)) {
      this.originalArrays.set(
        stateNode,
        stateNode.map((item: any) => this.targetOf(item)),
      );

      for (let i = 0; i < stateNode.length; i++) {
        if (snapshotNode?.[i]) this.indexAndLink(stateNode[i], snapshotNode[i]);
      }
      return;
    }

    if ("id" in stateNode && stateNode.id) {
      const snapshot = { ...snapshotNode };
      this.snapshotTargets.set(snapshot, stateNode);
      this.originalObjects.set(stateNode, snapshot);
    }

    for (const key in stateNode) {
      if (typeof stateNode[key] === "object") {
        this.indexAndLink(stateNode[key], snapshotNode?.[key]);
      }
    }
  }

  private targetOf(node: any): any {
    return this.reverseProxyCache.get(node) || this.snapshotTargets.get(node) || node;
  }

  private createProxy(target: any): any {
    if (typeof target !== "object" || target === null) return target;
    if (this.proxyCache.has(target)) return this.proxyCache.get(target);

    const isArray = Array.isArray(target);

    const handler: ProxyHandler<any> = {
      get: (tgt, prop, receiver) => {
        if (
          isArray &&
          typeof prop === "string" &&
          ["push", "pop", "shift", "unshift", "splice", "sort", "reverse"].includes(prop)
        ) {
          return (...args: any[]) => {
            const res = Reflect.get(tgt, prop, receiver).apply(
              tgt,
              args.map((arg) => this.targetOf(arg)),
            );
            this.checkArrayStructure(tgt);
            this.notify();
            return res;
          };
        }
        const val = Reflect.get(tgt, prop, receiver);
        return typeof val === "object" && val !== null ? this.createProxy(val) : val;
      },
      set: (tgt, prop, val) => {
        const res = Reflect.set(tgt, prop, this.targetOf(val));
        if (isArray) {
          if (prop === "length" || !isNaN(Number(prop))) this.checkArrayStructure(tgt);
        } else if (typeof prop === "string") {
          this.checkObjectField(tgt, prop);
        }
        this.notify();
        return res;
      },
      deleteProperty: (tgt, prop) => {
        const res = Reflect.deleteProperty(tgt, prop);
        if (isArray) this.checkArrayStructure(tgt);
        else if (typeof prop === "string") this.checkObjectField(tgt, prop);
        this.notify();
        return res;
      },
    };

    const proxy = new Proxy(target, handler);
    this.proxyCache.set(target, proxy);
    this.reverseProxyCache.set(proxy, target);
    return proxy;
  }

  private checkObjectField(target: any, prop: string) {
    if (!target.id || !this.originalObjects.has(target)) return;

    const originalVal = this.originalObjects.get(target)[prop];
    const currentVal = target[prop];

    let props = this.dirtyObjectProps.get(target);
    if (currentVal !== originalVal) {
      if (!props) {
        props = new Set();
        this.dirtyObjectProps.set(target, props);
      }
      props.add(prop);
    } else if (props) {
      props.delete(prop);
      if (props.size === 0) this.dirtyObjectProps.delete(target);
    }
  }

  private checkArrayStructure(array: any) {
    if (!this.originalArrays.has(array)) return;
    const originals = this.originalArrays.get(array)!;
    const currentIds = array.map((item: any) => this.targetOf(item));

    let dirty = originals.length !== currentIds.length;
    if (!dirty) {
      for (let i = 0; i < originals.length; i++) {
        if (originals[i] !== currentIds[i]) {
          dirty = true;
          break;
        }
      }
    }

    if (dirty) this.dirtyArrays.add(array);
    else this.dirtyArrays.delete(array);
  }

  private clearDirtyField(target: object, prop: string) {
    const props = this.dirtyObjectProps.get(target);
    if (!props) return;

    props.delete(prop);
    if (props.size === 0) {
      this.dirtyObjectProps.delete(target);
    }
  }

  private notify() {
    this.version += 1;
  }

  private removeIndexedState(node: any) {
    if (!node || typeof node !== "object") return;

    if (Array.isArray(node)) {
      this.originalArrays.delete(this.targetOf(node));
      for (const item of node) {
        this.removeIndexedState(item);
      }
      return;
    }

    if (node.id) {
      this.originalObjects.delete(this.targetOf(node));
    }

    for (const key in node) {
      if (typeof node[key] === "object") {
        this.removeIndexedState(node[key]);
      }
    }
  }

  private collectLiveState(node: any, ids: Set<object>, arrays: Set<object>) {
    if (!node || typeof node !== "object") return;

    if (Array.isArray(node)) {
      arrays.add(this.targetOf(node));
      for (const item of node) {
        this.collectLiveState(item, ids, arrays);
      }
      return;
    }

    if (node.id) {
      ids.add(this.targetOf(node));
    }

    for (const key in node) {
      if (typeof node[key] === "object") {
        this.collectLiveState(node[key], ids, arrays);
      }
    }
  }

  private pruneDirtyState() {
    const liveIds = new Set<object>();
    const liveArrays = new Set<object>();

    this.collectLiveState(this.state, liveIds, liveArrays);

    for (const id of Array.from(this.dirtyObjectProps.keys())) {
      if (!liveIds.has(id)) {
        this.dirtyObjectProps.delete(id);
      }
    }

    for (const array of Array.from(this.dirtyArrays)) {
      if (!liveArrays.has(array)) {
        this.dirtyArrays.delete(array);
        continue;
      }

      this.checkArrayStructure(array);
    }
  }

  private traverse(node: any, map: Map<object, any>) {
    if (Array.isArray(node)) {
      for (const child of node) this.traverse(child, map);
    } else if (node && typeof node === "object") {
      if (node.id) map.set(this.targetOf(node), node);
      for (const key in node) {
        const child = node[key];
        if (typeof child === "object") this.traverse(child, map);
      }
    }
  }

  get data() {
    const _ = this.version;
    return this.proxy;
  }

  get isDirty() {
    const _ = this.version;
    return this.dirtyObjectProps.size > 0 || this.dirtyArrays.size > 0;
  }

  get changes() {
    if (!this.isDirty) {
      return { added: [], deleted: [], mutated: [] };
    }

    const _ = this.version;
    const currentMap = new Map<object, any>();
    this.traverse(this.state, currentMap);

    const added: any[] = [];
    const deleted: any[] = [];
    const mutated: any[] = [];

    for (const [id, node] of currentMap) {
      if (!this.originalObjects.has(id)) {
        added.push(node);
      } else if (this.dirtyObjectProps.has(id)) {
        mutated.push(node);
      }
    }

    for (const [id, original] of this.originalObjects) {
      if (!currentMap.has(id)) {
        deleted.push(original);
      }
    }

    return $state.snapshot({ added, deleted, mutated });
  }

  reset(newData: T) {
    const snapshot = structuredClone(newData);

    if (Array.isArray(this.state) && Array.isArray(newData)) {
      this.state.length = 0;
      this.state.push(...newData);
    } else {
      const s = this.state as any;
      Object.keys(s).forEach((k) => delete s[k]);
      Object.assign(s, newData);
    }

    this.init(snapshot);
    this.proxy = this.createProxy(this.state);
    this.notify();
  }

  acknowledgeUpdate(object: any, persistedFields?: Record<string, any>) {
    if (!object || !object.id) return;
    const target = this.targetOf(object);
    const originalSnapshot = this.originalObjects.get(target);
    const fieldEntries = Object.entries(persistedFields ?? {});

    if (fieldEntries.length === 0) {
      if (originalSnapshot) {
        this.removeIndexedState(originalSnapshot);
      }

      this.clearDirtyStateFor(target);

      const snapshot = $state.snapshot(target);
      this.indexAndLink(target, snapshot);
      this.pruneDirtyState();
      this.notify();
      return;
    }

    const nextOriginalSnapshot = { ...(originalSnapshot ?? { id: object.id }) };

    for (const [field, value] of fieldEntries) {
      const previousValue = target[field];
      this.clearDirtyStateFor(previousValue);

      if (originalSnapshot) {
        this.removeIndexedState(originalSnapshot[field]);
      }

      target[field] = value;
    }

    const snapshot = $state.snapshot(target);

    nextOriginalSnapshot.id = snapshot.id;

    for (const [field] of fieldEntries) {
      nextOriginalSnapshot[field] = snapshot[field];
    }

    this.snapshotTargets.set(nextOriginalSnapshot, target);
    this.originalObjects.set(target, nextOriginalSnapshot);

    for (const [field] of fieldEntries) {
      this.indexAndLink(target[field], snapshot[field]);
      this.clearDirtyStateFor(target[field]);
      this.clearDirtyField(target, field);
    }

    this.pruneDirtyState();
    this.notify();
  }

  acknowledgeNewItem(array: any[], item: any, position: "start" | "end" = "end") {
    const targetArray = this.targetOf(array);
    if (!this.originalArrays.has(targetArray)) return;
    if (!item || !item.id) return;

    const originalIds = this.originalArrays.get(targetArray)!;
    // Svelte wraps newly inserted objects; use the actual stored instance.
    const targetItem = targetArray.find((candidate: any) => candidate?.id === item.id);
    if (!targetItem) return;
    const snapshot = $state.snapshot(targetItem);

    this.indexAndLink(targetItem, snapshot);

    if (position === "start") {
      originalIds.unshift(this.targetOf(targetItem));
    } else {
      originalIds.push(this.targetOf(targetItem));
    }

    this.clearDirtyStateFor(snapshot);

    this.checkArrayStructure(targetArray);
    this.notify();
  }

  acknowledgeDelete(array: any[], itemId: string) {
    const targetArray = this.targetOf(array);
    if (!this.originalArrays.has(targetArray)) return;

    const originalIds = this.originalArrays.get(targetArray)!;

    const index = originalIds.findIndex((item) => this.originalObjects.get(item)?.id === itemId);
    const removed = originalIds[index];
    if (index !== -1) {
      originalIds.splice(index, 1);
    }

    if (this.originalObjects.has(removed)) {
      const originalSnapshot = this.originalObjects.get(removed);
      this.clearDirtyStateFor(originalSnapshot);
      this.removeIndexedState(originalSnapshot);
    }

    this.checkArrayStructure(targetArray);
    this.notify();
  }

  private clearDirtyStateFor(node: any) {
    if (!node || typeof node !== "object") return;

    if (Array.isArray(node)) {
      this.dirtyArrays.delete(this.targetOf(node));
      for (const item of node) {
        this.clearDirtyStateFor(item);
      }
    } else {
      if (node.id) {
        this.dirtyObjectProps.delete(this.targetOf(node));
      }
      for (const key in node) {
        if (typeof node[key] === "object") {
          this.clearDirtyStateFor(node[key]);
        }
      }
    }
  }
}
