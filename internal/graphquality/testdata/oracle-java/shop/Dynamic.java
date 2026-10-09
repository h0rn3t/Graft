package shop;

import java.lang.reflect.Method;

final class Dynamic {
    private Dynamic() {
    }

    static Object byName(Object target, String name) throws Exception {
        Method method = target.getClass().getMethod(name);
        return method.invoke(target);
    }

    static String saveAll(Saver saver) {
        return saver.save();
    }

    static void fire(Listener listener, String item) {
        listener.onSave(item);
    }
}
