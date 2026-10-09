package shop;

public class Repo extends BaseRepo implements Saver {
    private final Store store;

    public Repo(Store store) {
        this.store = store;
    }

    public static Repo create() {
        return new Repo(new Store());
    }

    @Override
    public String save() {
        store.put(Util.format("row"));
        this.store.put(Util.format("row", 4));
        return super.save();
    }

    public String flush(Cache cache) {
        cache.put("row");
        return describe();
    }

    public Listener listener(Store target) {
        return item -> target.put(item);
    }

    public Listener anonymous() {
        return new Listener() {
            @Override
            public void onSave(String item) {
                new Cache().put(item);
            }
        };
    }
}
