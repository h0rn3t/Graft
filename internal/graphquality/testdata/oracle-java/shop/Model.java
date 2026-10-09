package shop;

interface Saver {
    String save();
}

interface Listener {
    void onSave(String item);
}

class Store {
    private int count;

    void put(String item) {
        count += item.length();
        this.touch();
    }

    int touch() {
        return count;
    }
}

class Cache {
    String put(String item) {
        return item;
    }

    String describe() {
        return "cache";
    }
}

class BaseRepo {
    String save() {
        return "saved";
    }

    String describe() {
        return "repo";
    }
}
