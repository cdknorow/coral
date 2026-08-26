export class NavigationHistory {
    constructor(limit = 50) {
        this.limit = limit;
        this.entries = [];
    }

    record(entry, options = {}) {
        if (options.recordHistory === false || !entry) return false;
        this.entries.push({ ...entry });
        if (this.entries.length > this.limit) {
            this.entries.splice(0, this.entries.length - this.limit);
        }
        return true;
    }

    pop() {
        return this.entries.pop() || null;
    }

    get size() {
        return this.entries.length;
    }
}
