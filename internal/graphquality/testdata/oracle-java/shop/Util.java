package shop;

public final class Util {
    private Util() {
    }

    public static String format(String value) {
        return format(value, 0);
    }

    public static String format(String value, int width) {
        return pad(value, width);
    }

    static String pad(String value, int width) {
        return value;
    }
}
